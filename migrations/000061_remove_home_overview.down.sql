INSERT INTO `bp_menu`
  (`id`, `pid`, `name`, `path`, `component`, `redirect`, `type`, `auth_code`, `meta`, `status`)
VALUES
  (141, 0, 'HomeOverview', '/dashboard/home-overview', '/dashboard/workspace/index', NULL, 'catalog', 'home:overview:view', JSON_OBJECT('title', 'system.home.overviewTitle', 'icon', 'lucide:layout-dashboard', 'order', 9989), 1)
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
SELECT `id`, 141 FROM `bp_role` WHERE `role_key` = 'admin';
