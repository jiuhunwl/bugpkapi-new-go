CREATE TABLE IF NOT EXISTS `bp_auth_qq_login_config` (
  `id` tinyint NOT NULL DEFAULT 1,
  `enabled` tinyint NOT NULL DEFAULT 0,
  `app_id` varchar(128) NOT NULL DEFAULT '',
  `app_key` varchar(255) NOT NULL DEFAULT '',
  `redirect_uri` varchar(1024) NOT NULL DEFAULT '',
  `frontend_base_url` varchar(1024) NOT NULL DEFAULT '',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  CONSTRAINT `chk_bp_auth_qq_login_config_singleton` CHECK (`id` = 1)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='QQ login reserved config';

INSERT INTO `bp_auth_qq_login_config` (`id`, `enabled`, `frontend_base_url`)
VALUES (1, 0, '')
ON DUPLICATE KEY UPDATE `id` = `id`;

INSERT INTO `bp_menu`
  (`id`, `pid`, `name`, `path`, `component`, `redirect`, `type`, `auth_code`, `meta`, `status`)
VALUES
  (150, 30, 'SystemRegisterOpenConfig', '/system/register', '/system/register/index', NULL, 'menu', 'system:register:open-config', JSON_OBJECT('title', '注册开放设置', 'icon', 'mdi:account-plus-outline', 'order', 2), 1),
  (151, 150, 'SystemRegisterOpenConfigUpdate', '', NULL, NULL, 'button', 'system:register:open-config:update', JSON_OBJECT('title', '保存注册开放设置', 'order', 1), 1),
  (152, 30, 'SystemRegisterEmailConfig', '/system/register/email', '/system/register/email-config', NULL, 'menu', 'system:register:email-config', JSON_OBJECT('title', '邮箱验证码设置', 'icon', 'mdi:email-cog-outline', 'order', 3), 1),
  (153, 152, 'SystemRegisterEmailConfigUpdate', '', NULL, NULL, 'button', 'system:register:email-config:update', JSON_OBJECT('title', '保存邮箱验证码设置', 'order', 1), 1),
  (154, 152, 'SystemRegisterEmailConfigTest', '', NULL, NULL, 'button', 'system:register:email-config:test', JSON_OBJECT('title', '测试邮箱验证码设置', 'order', 2), 1),
  (155, 30, 'SystemRegisterMobileConfig', '/system/register/mobile', '/system/register/mobile-config', NULL, 'menu', 'system:register:mobile-config', JSON_OBJECT('title', '短信验证码设置', 'icon', 'mdi:cellphone-cog', 'order', 4), 1),
  (156, 155, 'SystemRegisterMobileConfigUpdate', '', NULL, NULL, 'button', 'system:register:mobile-config:update', JSON_OBJECT('title', '保存短信验证码设置', 'order', 1), 1),
  (157, 155, 'SystemRegisterMobileConfigTest', '', NULL, NULL, 'button', 'system:register:mobile-config:test', JSON_OBJECT('title', '测试短信验证码设置', 'order', 2), 1),
  (158, 30, 'SystemQuickLoginConfig', '/system/login/quick', '/system/login/quick-login', NULL, 'menu', 'system:login:qq-config', JSON_OBJECT('title', '快捷登录设置', 'icon', 'mdi:login-variant', 'order', 5), 1),
  (159, 158, 'SystemQuickLoginConfigUpdate', '', NULL, NULL, 'button', 'system:login:qq-config:update', JSON_OBJECT('title', '保存快捷登录设置', 'order', 1), 1),
  (160, 30, 'SystemLogDelete', '/system/log-delete', '/system/log-delete/index', NULL, 'menu', 'log:call:cleanup', JSON_OBJECT('title', '日志清理设置', 'icon', 'mdi:delete-clock-outline', 'order', 6), 1)
ON DUPLICATE KEY UPDATE
  `pid` = VALUES(`pid`),
  `path` = VALUES(`path`),
  `component` = VALUES(`component`),
  `redirect` = VALUES(`redirect`),
  `type` = VALUES(`type`),
  `auth_code` = VALUES(`auth_code`),
  `meta` = VALUES(`meta`),
  `status` = VALUES(`status`);

INSERT IGNORE INTO `bp_role_menu` (`role_id`, `menu_id`)
SELECT 1, id FROM `bp_menu` WHERE id BETWEEN 150 AND 160;
