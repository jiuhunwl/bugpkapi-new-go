INSERT INTO `bp_menu`
  (`id`, `pid`, `name`, `path`, `component`, `redirect`, `type`, `auth_code`, `meta`, `status`)
VALUES
  (91, 90, 'AdminDashboardStats', '/dashboard-stats/admin', '/dashboard/workspace/index', NULL, 'menu', 'dashboard:admin:view', JSON_OBJECT('title', '管理看板', 'order', 1), 1),
  (171, 90, 'RiskControlEarth', '/risk-control/earth', '/risk-control/earth', NULL, 'menu', 'risk-control:earth:view', JSON_OBJECT('title', 'system.riskControl.earthTitle', 'icon', 'lucide:globe-2', 'order', 4), 1),
  (173, 171, 'RiskControlEarthView', '', NULL, NULL, 'button', 'risk-control:earth:view', JSON_OBJECT('title', '地球视角监控', 'order', 1), 1)
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
SELECT 1, `id` FROM `bp_menu` WHERE `id` IN (91, 171, 173);
