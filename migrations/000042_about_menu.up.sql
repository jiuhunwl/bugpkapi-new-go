-- 后台「系统设置」菜单补充「关于系统」入口（对应 /system/about 前端页面）。
-- 前端路由已静态注册（router/routes/modules/system.ts），这里补 DB 菜单，让侧边栏可见。
INSERT INTO `bp_menu`
  (`id`, `pid`, `name`, `path`, `component`, `redirect`, `type`, `auth_code`, `meta`, `status`)
VALUES
  (178, 0, 'SystemAbout', '/system/about', '/system/about/index', NULL, 'menu', 'system:about:view', JSON_OBJECT('title', 'system.about.title', 'icon', 'mdi:information-outline', 'order', 9999), 1)
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
SELECT 1, id FROM `bp_menu` WHERE id = 178;
