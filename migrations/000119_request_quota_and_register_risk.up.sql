-- 请求配额与注册防刷（2026-11）。
--
-- 1) bp_role 增加「阶梯式请求配额」：分 / 时 / 日 / 月 四层上限，
--    由 user.primary_role_id 决定生效配置，因此用户组即配额档位。
--    request_quota_count_mode 控制计量口径：
--      attempt = 从请求进入配额闸门开始计量，失败也计；
--      success = 仅成功返回的调用计量。
--    默认全部关闭（request_quota_enabled=0）；管理员在角色管理里按组启用。
--
-- 2) bp_auth_register_config 增加注册风控参数：
--    新账号首日调用上限、设备/网段日注册上限、评分阈值、影子模式。
--    默认首日额度 0（不限），风险评分开启但处于影子模式：只记录不拒绝/不隔离。
--
-- 3) bp_register_event 留存每一次注册决策（allow/quarantine/reject），
--    用于调参与取证；所有 IP/设备只落 keyed hash，不存明文。
-- 4) bp_user_risk_state 承载「隔离观察」：评分达到隔离阈值的新账号
--    可正常登录，但 24h 内不能调用接口；到期自动恢复，无需 worker。
-- 5) bp_auth_email_code 增加 attempts，6 位验证码不再可无限试。
ALTER TABLE `bp_role`
  ADD COLUMN `request_quota_enabled` tinyint NOT NULL DEFAULT 0
    COMMENT 'Whether role request quota is enforced; 0 = unlimited' AFTER `self_register_enabled`,
  ADD COLUMN `request_quota_minute` bigint NOT NULL DEFAULT 60
    COMMENT 'Per-user request cap per minute; 0 = unlimited' AFTER `request_quota_enabled`,
  ADD COLUMN `request_quota_hour` bigint NOT NULL DEFAULT 600
    COMMENT 'Per-user request cap per hour; 0 = unlimited' AFTER `request_quota_minute`,
  ADD COLUMN `request_quota_day` bigint NOT NULL DEFAULT 1000
    COMMENT 'Per-user request cap per day; 0 = unlimited' AFTER `request_quota_hour`,
  ADD COLUMN `request_quota_month` bigint NOT NULL DEFAULT 20000
    COMMENT 'Per-user request cap per calendar month; 0 = unlimited' AFTER `request_quota_day`,
  ADD COLUMN `request_quota_count_mode` varchar(16) NOT NULL DEFAULT 'attempt'
    COMMENT 'attempt = count every admitted request, success = count successful calls only' AFTER `request_quota_month`,
  ADD CONSTRAINT `chk_bp_role_quota_count_mode`
    CHECK (`request_quota_count_mode` IN ('attempt', 'success')),
  ADD CONSTRAINT `chk_bp_role_quota_non_negative`
    CHECK (`request_quota_minute` >= 0 AND `request_quota_hour` >= 0
           AND `request_quota_day` >= 0 AND `request_quota_month` >= 0);

-- 默认所有用户组都不启用配额；需要时在「角色管理」逐个打开。
UPDATE `bp_role` SET `request_quota_enabled` = 0;

ALTER TABLE `bp_auth_register_config`
  ADD COLUMN `new_account_day_limit` bigint NOT NULL DEFAULT 0
    COMMENT 'Calls allowed during the first 24h after registration; 0 = unlimited' AFTER `register_ip_max_accounts`,
  ADD COLUMN `register_device_day_limit` int NOT NULL DEFAULT 5
    COMMENT 'Accounts allowed per signed device cookie per 24h; 0 = unlimited' AFTER `new_account_day_limit`,
  ADD COLUMN `register_ip_prefix_day_limit` int NOT NULL DEFAULT 10
    COMMENT 'Accounts allowed per IPv4 /24 or IPv6 /64 per 24h; 0 = unlimited' AFTER `register_device_day_limit`,
  ADD COLUMN `register_risk_enabled` tinyint NOT NULL DEFAULT 1
    COMMENT 'Enable registration risk scoring' AFTER `register_ip_prefix_day_limit`,
  ADD COLUMN `register_risk_shadow_mode` tinyint NOT NULL DEFAULT 1
    COMMENT '1 = record scores but never reject/quarantine' AFTER `register_risk_enabled`,
  ADD COLUMN `register_risk_quarantine_score` int NOT NULL DEFAULT 40
    COMMENT 'Score at or above which a new account is quarantined for 24h' AFTER `register_risk_shadow_mode`,
  ADD COLUMN `register_risk_reject_score` int NOT NULL DEFAULT 75
    COMMENT 'Score at or above which registration returns a fake success' AFTER `register_risk_quarantine_score`;

ALTER TABLE `bp_auth_email_code`
  ADD COLUMN `attempts` tinyint unsigned NOT NULL DEFAULT 0
    COMMENT 'Failed verification attempts; code is revoked at 5' AFTER `consumed_at`;

CREATE TABLE IF NOT EXISTS `bp_register_event` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `decision` varchar(16) NOT NULL COMMENT 'allow/quarantine/reject',
  `score` int NOT NULL DEFAULT 0 COMMENT 'Risk score 0-120',
  `user_id` bigint unsigned DEFAULT NULL COMMENT 'Only set when an account was created',
  `username` varchar(64) NOT NULL DEFAULT '',
  `email_domain` varchar(255) NOT NULL DEFAULT '',
  `client_ip_hash` binary(32) NOT NULL COMMENT 'iphash.RiskDigest(client ip)',
  `prefix_hash` binary(32) NOT NULL COMMENT 'iphash.PrefixDigest(client ip), /24 or /64',
  `device_hash` binary(32) NOT NULL COMMENT 'sha256(signed device cookie id)',
  `user_agent` varchar(512) NOT NULL DEFAULT '',
  `accept_language` varchar(128) NOT NULL DEFAULT '',
  `elapsed_ms` int NOT NULL DEFAULT 0 COMMENT 'Captcha issue to register submit',
  `signals` json DEFAULT NULL COMMENT 'Matched risk signals for tuning',
  PRIMARY KEY (`id`),
  KEY `idx_bp_register_event_created` (`created_at`),
  KEY `idx_bp_register_event_prefix` (`prefix_hash`, `created_at`),
  KEY `idx_bp_register_event_device` (`device_hash`, `created_at`),
  KEY `idx_bp_register_event_domain` (`email_domain`, `created_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='Registration decision stream (no plaintext IP)';

CREATE TABLE IF NOT EXISTS `bp_user_risk_state` (
  `user_id` bigint unsigned NOT NULL,
  `quarantine_until` datetime NOT NULL COMMENT 'API calls are rejected until this instant',
  `risk_score` int NOT NULL DEFAULT 0,
  `reason` varchar(255) NOT NULL DEFAULT '',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`user_id`),
  KEY `idx_bp_user_risk_state_until` (`quarantine_until`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='Account risk quarantine state';

-- 一批高置信度一次性/临时邮箱域。只补黑名单，不碰管理员配置的域名。
INSERT IGNORE INTO `bp_email_domain_rule` (`rule_kind`, `domain`) VALUES
  ('blacklist', '10minutemail.com'), ('blacklist', '1secmail.com'),
  ('blacklist', '234.ee'), ('blacklist', 'daodao.app'),
  ('blacklist', 'discard.email'), ('blacklist', 'dispostable.com'),
  ('blacklist', 'dropmail.me'), ('blacklist', 'emailondeck.com'),
  ('blacklist', 'fakeinbox.com'), ('blacklist', 'getnada.com'),
  ('blacklist', 'guerrillamail.com'), ('blacklist', '*.guerrillamail.com'),
  ('blacklist', 'inboxes.com'), ('blacklist', 'itime.day'),
  ('blacklist', 'linshiyou.com'), ('blacklist', 'lnovic.com'),
  ('blacklist', 'mail.tm'), ('blacklist', 'maildrop.cc'),
  ('blacklist', 'mailinator.com'), ('blacklist', '*.mailinator.com'),
  ('blacklist', 'mailnesia.com'), ('blacklist', 'mintemail.com'),
  ('blacklist', 'mohmal.com'), ('blacklist', 'moakt.com'),
  ('blacklist', 'sharklasers.com'), ('blacklist', 'spambox.us'),
  ('blacklist', 'temp-mail.org'), ('blacklist', '*.temp-mail.org'),
  ('blacklist', 'tempmail.com'), ('blacklist', 'tempr.email'),
  ('blacklist', 'textdiff.net'), ('blacklist', 'throwawaymail.com'),
  ('blacklist', 'trashmail.com'), ('blacklist', 'yopmail.com'),
  ('blacklist', '*.yopmail.com'), ('blacklist', 'youxiang.dev');
