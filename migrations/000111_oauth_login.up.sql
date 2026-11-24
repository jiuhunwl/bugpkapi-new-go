-- Third-party login providers.
--
-- Provider configuration is data-driven so adding a standard OAuth2/OIDC
-- provider does not require a backend deploy. The legacy QQ config table is
-- intentionally kept for one release: its values are copied into the `qq`
-- provider row below, but the provider stays disabled until an administrator
-- verifies the redirect URI and enables it.

CREATE TABLE IF NOT EXISTS `bp_auth_oauth_config` (
  `id` tinyint NOT NULL DEFAULT 1,
  `frontend_base_url` varchar(1024) NOT NULL DEFAULT '' COMMENT '前端基础地址，登录回调后跳转使用',
  `register_ip_daily_limit` int NOT NULL DEFAULT 10 COMMENT '第三方注册每 IP 每日上限，0=不限制',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  CONSTRAINT `chk_bp_auth_oauth_config_singleton` CHECK (`id` = 1)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='第三方登录全局配置';

INSERT INTO `bp_auth_oauth_config` (`id`)
VALUES (1)
ON DUPLICATE KEY UPDATE `id` = `id`;

CREATE TABLE IF NOT EXISTS `bp_auth_oauth_provider` (
  `id` bigint NOT NULL AUTO_INCREMENT,
  `provider_key` varchar(32) NOT NULL COMMENT '对外唯一标识，如 qq/wechat/github/linuxdo',
  `display_name` varchar(64) NOT NULL,
  `icon_key` varchar(32) NOT NULL DEFAULT 'generic',
  `driver` varchar(32) NOT NULL COMMENT 'qq/wechat/github/oauth2/oidc',
  `provider_group` varchar(32) NOT NULL DEFAULT '' COMMENT '同厂商归并，用于 unionid 兜底',
  `enabled` tinyint NOT NULL DEFAULT 0,
  `register_mode` varchar(16) NOT NULL DEFAULT 'off' COMMENT 'off/confirm/auto',
  `link_by_email` tinyint NOT NULL DEFAULT 0 COMMENT '是否允许按已验证邮箱合并，默认关闭',
  `require_verified_email` tinyint NOT NULL DEFAULT 0,
  `client_id` varchar(255) NOT NULL DEFAULT '',
  `client_secret` varchar(1024) NOT NULL DEFAULT '' COMMENT 'secretbox 加密存储',
  `issuer` varchar(512) NOT NULL DEFAULT '',
  `authorization_endpoint` varchar(1024) NOT NULL DEFAULT '',
  `token_endpoint` varchar(1024) NOT NULL DEFAULT '',
  `userinfo_endpoint` varchar(1024) NOT NULL DEFAULT '',
  `jwks_uri` varchar(1024) NOT NULL DEFAULT '',
  `scopes` varchar(512) NOT NULL DEFAULT '',
  `redirect_uri` varchar(1024) NOT NULL DEFAULT '' COMMENT '必须与平台后台完全一致',
  `extra_config` json DEFAULT NULL COMMENT '字段映射、token 认证方式等',
  `sort_no` int NOT NULL DEFAULT 0,
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_bp_auth_oauth_provider_key` (`provider_key`),
  KEY `idx_bp_auth_oauth_provider_enabled` (`enabled`, `sort_no`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='第三方登录 Provider 配置';

INSERT INTO `bp_auth_oauth_provider`
  (`provider_key`, `display_name`, `icon_key`, `driver`, `provider_group`, `enabled`, `register_mode`, `scopes`, `issuer`, `authorization_endpoint`, `token_endpoint`, `userinfo_endpoint`, `sort_no`)
VALUES
  ('qq', 'QQ', 'qq', 'qq', 'qq_connect', 0, 'confirm', 'get_user_info', '', '', '', '', 10),
  ('wechat', '微信', 'wechat', 'wechat', 'wechat_open', 0, 'confirm', 'snsapi_login', '', '', '', '', 20),
  ('github', 'GitHub', 'github', 'github', 'github', 0, 'confirm', 'read:user user:email', '', 'https://github.com/login/oauth/authorize', 'https://github.com/login/oauth/access_token', 'https://api.github.com/user', 30),
  ('linuxdo', 'LinuxDo', 'linuxdo', 'oauth2', 'linuxdo', 0, 'confirm', '', 'https://linux.do', 'https://connect.linux.do/oauth2/authorize', 'https://connect.linux.do/oauth2/token', 'https://connect.linux.do/api/user', 40)
ON DUPLICATE KEY UPDATE
  `display_name` = VALUES(`display_name`),
  `icon_key` = VALUES(`icon_key`),
  `driver` = VALUES(`driver`),
  `provider_group` = VALUES(`provider_group`),
  `scopes` = VALUES(`scopes`),
  `issuer` = CASE WHEN `issuer` = '' THEN VALUES(`issuer`) ELSE `issuer` END,
  `authorization_endpoint` = CASE WHEN `authorization_endpoint` = '' THEN VALUES(`authorization_endpoint`) ELSE `authorization_endpoint` END,
  `token_endpoint` = CASE WHEN `token_endpoint` = '' THEN VALUES(`token_endpoint`) ELSE `token_endpoint` END,
  `userinfo_endpoint` = CASE WHEN `userinfo_endpoint` = '' THEN VALUES(`userinfo_endpoint`) ELSE `userinfo_endpoint` END,
  `sort_no` = VALUES(`sort_no`);

-- LinuxDo is a standard OAuth2 service with a custom userinfo payload. The
-- mapping keeps the driver generic: another provider only needs a new row.
UPDATE `bp_auth_oauth_provider`
SET `extra_config` = JSON_OBJECT(
  'subject_field', 'id',
  'username_field', 'username',
  'nickname_field', 'name',
  'avatar_field', 'avatar_template',
  'email_field', 'email',
  'email_verified_field', 'email_verified',
  'trust_level_field', 'trust_level',
  'active_field', 'active',
  'silenced_field', 'silenced',
  'token_auth_style', 'client_secret_post',
  'userinfo_auth_style', 'bearer',
  'avatar_size', 96,
  'pkce', FALSE
)
WHERE `provider_key` = 'linuxdo' AND `extra_config` IS NULL;

CREATE TABLE IF NOT EXISTS `bp_auth_oauth_identity` (
  `id` bigint NOT NULL AUTO_INCREMENT,
  `user_id` bigint NOT NULL,
  `provider_key` varchar(32) NOT NULL,
  `provider_group` varchar(32) NOT NULL DEFAULT '',
  `subject` varchar(255) NOT NULL COMMENT 'openid / GitHub id / LinuxDo id',
  `union_id` varchar(255) NOT NULL DEFAULT '',
  `nickname` varchar(128) NOT NULL DEFAULT '',
  `avatar` varchar(1024) NOT NULL DEFAULT '',
  `email` varchar(255) NOT NULL DEFAULT '',
  `email_verified` tinyint NOT NULL DEFAULT 0,
  `bound_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `last_login_at` datetime DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_bp_auth_oauth_identity_subject` (`provider_key`, `subject`),
  UNIQUE KEY `uk_bp_auth_oauth_identity_user_provider` (`user_id`, `provider_key`),
  KEY `idx_bp_auth_oauth_identity_union` (`provider_group`, `union_id`),
  KEY `idx_bp_auth_oauth_identity_user` (`user_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='第三方身份绑定';

ALTER TABLE `bp_user`
  ADD COLUMN `password_set` tinyint NOT NULL DEFAULT 1 COMMENT '是否设置过密码；OAuth 自动建号为 0' AFTER `password_hash`;

-- Copy the legacy QQ scaffold without enabling it. client_secret is copied as
-- stored: secretbox.DecryptCompatible accepts both legacy plaintext and the
-- current enc:v1 ciphertext.
UPDATE `bp_auth_oauth_provider` p
JOIN `bp_auth_qq_login_config` q ON q.id = 1
SET
  p.client_id = CASE WHEN p.client_id = '' THEN q.app_id ELSE p.client_id END,
  p.client_secret = CASE WHEN p.client_secret = '' THEN q.app_key ELSE p.client_secret END,
  p.redirect_uri = CASE WHEN p.redirect_uri = '' THEN q.redirect_uri ELSE p.redirect_uri END
WHERE p.provider_key = 'qq';

UPDATE `bp_auth_oauth_config` c
JOIN `bp_auth_qq_login_config` q ON q.id = 1
SET c.frontend_base_url = CASE WHEN c.frontend_base_url = '' THEN q.frontend_base_url ELSE c.frontend_base_url END
WHERE c.id = 1;

-- Reuse the existing "quick login" menu entry instead of creating a duplicate
-- route. Role grants are keyed by menu id, so they survive the code rename.
UPDATE `bp_menu`
SET `name` = 'SystemOAuthLoginConfig',
    `auth_code` = 'system:login:oauth-config',
    `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.title', '第三方登录', '$.icon', 'mdi:login-variant'),
    `status` = 1
WHERE `id` = 158;

UPDATE `bp_menu`
SET `name` = 'SystemOAuthLoginConfigUpdate',
    `auth_code` = 'system:login:oauth-config:update',
    `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.title', '保存第三方登录设置'),
    `status` = 1
WHERE `id` = 159;

INSERT IGNORE INTO `bp_role_menu` (`role_id`, `menu_id`)
SELECT 1, `id` FROM `bp_menu` WHERE `id` IN (158, 159);
