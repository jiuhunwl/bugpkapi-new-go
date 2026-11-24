-- 后台「系统设置」菜单补充「验证码配置」入口（对应 /system/captcha-config 前端页面）。
-- 前端路由已静态注册（router/routes/modules/system.ts），这里补 DB 菜单，让侧边栏可见。
INSERT INTO `bp_menu`
  (`id`, `pid`, `name`, `path`, `component`, `redirect`, `type`, `auth_code`, `meta`, `status`)
VALUES
  (179, 30, 'SystemCaptchaConfig', '/system/captcha-config', '/system/captcha-config/index', NULL, 'menu', 'system:captcha-config:view', JSON_OBJECT('title', '验证码配置', 'icon', 'mdi:shield-key-outline', 'order', 9), 1),
  (180, 179, 'SystemCaptchaConfigUpdate', '', NULL, NULL, 'button', 'system:captcha-config:update', JSON_OBJECT('title', '保存验证码配置', 'order', 1), 1)
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
SELECT 1, id FROM `bp_menu` WHERE id BETWEEN 179 AND 180;
