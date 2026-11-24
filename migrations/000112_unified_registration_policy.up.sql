-- Unified registration policy.
--
-- The local registration page and the third-party login page used to own
-- separate registration switches and limits. From 1.2.46 the register config
-- is the single global policy source; every provider may override role, gift
-- points, IP limits, verified-email policy and provider risk gates.

ALTER TABLE `bp_auth_register_config`
  ADD COLUMN `register_ip_day_limit` int NOT NULL DEFAULT 0
    COMMENT '本地注册每 IP 每日上限，0=不限制' AFTER `register_ip_hour_limit`,
  ADD COLUMN `oauth_register_enabled` tinyint NOT NULL DEFAULT 1
    COMMENT '第三方注册总开关' AFTER `register_enabled`,
  ADD COLUMN `oauth_register_mode_default` varchar(16) NOT NULL DEFAULT 'confirm'
    COMMENT '第三方注册默认模式 off/confirm/auto' AFTER `oauth_register_enabled`,
  ADD COLUMN `oauth_register_ip_hour_limit` int NOT NULL DEFAULT 0
    COMMENT '第三方注册每 IP 每小时上限，0=不限制' AFTER `register_ip_day_limit`,
  ADD COLUMN `oauth_register_ip_day_limit` int NOT NULL DEFAULT 10
    COMMENT '第三方注册每 IP 每日上限，0=不限制' AFTER `oauth_register_ip_hour_limit`,
  ADD COLUMN `oauth_register_ip_max_accounts` int NOT NULL DEFAULT 0
    COMMENT '第三方注册每 IP 每平台终身上限，0=不限制' AFTER `oauth_register_ip_day_limit`,
  ADD COLUMN `oauth_require_verified_email` tinyint NOT NULL DEFAULT 0
    COMMENT '第三方注册是否默认要求已验证邮箱' AFTER `oauth_register_ip_max_accounts`;

-- Preserve the value from the old third-party global config. The old column is
-- kept for one release so a rollback to 1.2.45 still has its input.
UPDATE `bp_auth_register_config` c
JOIN `bp_auth_oauth_config` o ON o.id = 1
SET c.oauth_register_ip_day_limit = o.register_ip_daily_limit
WHERE c.id = 1;

ALTER TABLE `bp_auth_oauth_provider`
  ADD COLUMN `default_role_id` bigint NOT NULL DEFAULT 0
    COMMENT '注册默认角色，0=跟随全局' AFTER `register_mode`,
  ADD COLUMN `register_gift_points` bigint NOT NULL DEFAULT -1
    COMMENT '注册赠送点数，-1=跟随全局' AFTER `default_role_id`,
  ADD COLUMN `register_ip_hour_limit` int NOT NULL DEFAULT -1
    COMMENT '每 IP 每小时注册上限，-1=跟随全局' AFTER `register_gift_points`,
  ADD COLUMN `register_ip_day_limit` int NOT NULL DEFAULT -1
    COMMENT '每 IP 每日注册上限，-1=跟随全局' AFTER `register_ip_hour_limit`,
  ADD COLUMN `register_ip_max_accounts` int NOT NULL DEFAULT -1
    COMMENT '每 IP 每平台终身上限，-1=跟随全局' AFTER `register_ip_day_limit`,
  ADD COLUMN `require_verified_email_mode` tinyint NOT NULL DEFAULT -1
    COMMENT '是否要求已验证邮箱：-1=跟随全局，0=不要求，1=要求' AFTER `require_verified_email`,
  ADD COLUMN `min_account_age_days` int NOT NULL DEFAULT 0
    COMMENT '第三方账号最小注册天数，0=不限制' AFTER `require_verified_email_mode`,
  ADD COLUMN `min_trust_level` int NOT NULL DEFAULT 0
    COMMENT '第三方平台最低信任等级，0=不限制' AFTER `min_account_age_days`,
  ADD COLUMN `require_active` tinyint NOT NULL DEFAULT 1
    COMMENT '要求第三方账号处于活跃状态' AFTER `min_trust_level`,
  ADD COLUMN `reject_silenced` tinyint NOT NULL DEFAULT 1
    COMMENT '拒绝被禁言/受限的第三方账号' AFTER `require_active`;

UPDATE `bp_auth_oauth_provider`
SET `require_verified_email_mode` = CASE WHEN `require_verified_email` = 1 THEN 1 ELSE -1 END;

-- Per-channel lifetime registration quota. The existing bp_register_ip_used
-- remains local-only for backward compatibility; OAuth uses this counter so
-- its lifetime cap can be enforced atomically per provider.
CREATE TABLE IF NOT EXISTS `bp_register_ip_usage` (
  `ip_hash` binary(32) NOT NULL,
  `channel` varchar(64) NOT NULL COMMENT 'local 或 oauth:<provider_key>',
  `used` int NOT NULL DEFAULT 0,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`ip_hash`, `channel`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='注册 IP 分渠道累计配额（keyed hash，无明文 IP）';
