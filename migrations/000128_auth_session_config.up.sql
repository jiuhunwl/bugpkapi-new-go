-- 登录态有效期后台可配置：
--   bp_auth_session_config 单例行（CHECK id=1，模式与 bp_auth_register_config 一致），
--   存 access token 分钟数与 refresh token 天数；运行时由 systemsettings 域读写，
--   保存后通过观察者回调推给 auth.JWTService 覆盖 config.yaml/env 里的静态值。
-- 默认值与 config.AuthConfig 默认（30 分钟 / 7 天）保持一致，未配置时行为不变。
CREATE TABLE IF NOT EXISTS `bp_auth_session_config` (
  `id` BIGINT UNSIGNED NOT NULL,
  `access_token_minutes` INT UNSIGNED NOT NULL DEFAULT 30,
  `refresh_token_days` INT UNSIGNED NOT NULL DEFAULT 7,
  `updated_at` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  CONSTRAINT `chk_bp_auth_session_config_singleton` CHECK (`id` = 1)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_unicode_ci;

INSERT IGNORE INTO `bp_auth_session_config` (`id`) VALUES (1);

-- 后台「系统设置」补充「登录态配置」入口（前端路由 /system/session-config 静态注册）。
-- 菜单 ID 取 900110/900111：当前最大种子 ID 为 900100（工单分组），留 10 的间隔防撞。
INSERT INTO `bp_menu`
  (`id`, `pid`, `name`, `path`, `component`, `redirect`, `type`, `auth_code`, `meta`, `status`)
VALUES
  (900110, 30, 'SystemSessionConfig', '/system/session-config', '/system/session-config/index', NULL, 'menu', 'system:session-config:view',
   JSON_OBJECT('title', 'sessionConfig.title', 'icon', 'mdi:timer-outline', 'order', 45, 'audience', 'admin'), 1),
  (900111, 900110, 'SystemSessionConfigUpdate', '', NULL, NULL, 'button', 'system:session-config:update',
   JSON_OBJECT('title', 'sessionConfig.save', 'order', 1), 1)
ON DUPLICATE KEY UPDATE
  `pid` = VALUES(`pid`),
  `path` = VALUES(`path`),
  `component` = VALUES(`component`),
  `redirect` = VALUES(`redirect`),
  `type` = VALUES(`type`),
  `auth_code` = VALUES(`auth_code`),
  `meta` = VALUES(`meta`),
  `status` = VALUES(`status`);

-- 授权给管理员角色（role_id=1），与其它菜单种子保持一致。
INSERT IGNORE INTO `bp_role_menu` (`role_id`, `menu_id`)
SELECT 1, id FROM `bp_menu` WHERE id BETWEEN 900110 AND 900111;
