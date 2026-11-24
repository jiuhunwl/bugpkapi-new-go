-- Align route paths with the console URLs used by the workbench and provide
-- role-specific navigation without duplicating shared feature routes.
UPDATE `bp_menu`
SET `path` = '/console/interfaces'
WHERE `id` = 41 AND `name` = 'InterfaceList';

UPDATE `bp_menu`
SET `path` = '/console/call-logs'
WHERE `id` = 50 AND `name` = 'InterfaceCallLog';

UPDATE `bp_menu`
SET `meta` = JSON_SET(
  COALESCE(`meta`, JSON_OBJECT()),
  '$.adminTitle', 'navigation.admin.interfaceGateway',
  '$.adminOrder', 2,
  '$.userTitle', 'navigation.user.interfaceServices',
  '$.userOrder', 2
)
WHERE `id` = 40 AND `name` = 'InterfaceManagement';

UPDATE `bp_menu`
SET `pid` = 40,
    `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.order', 3)
WHERE `id` = 60 AND `name` = 'ApiKeyManagement';

UPDATE `bp_menu`
SET `meta` = JSON_SET(
  COALESCE(`meta`, JSON_OBJECT()),
  '$.title', 'navigation.admin.userPermissions',
  '$.order', 3,
  '$.audience', 'admin'
)
WHERE `id` = 10 AND `name` = 'UserManagement';

UPDATE `bp_menu`
SET `meta` = JSON_SET(
  COALESCE(`meta`, JSON_OBJECT()),
  '$.title', 'navigation.admin.packagesTransactions',
  '$.order', 4,
  '$.audience', 'admin'
)
WHERE `id` = 70 AND `name` = 'PackageManagement';

UPDATE `bp_menu`
SET `pid` = 70,
    `meta` = JSON_SET(
      COALESCE(`meta`, JSON_OBJECT()),
      '$.order', 5,
      '$.audience', 'admin'
    )
WHERE `id` = 100 AND `name` = 'PaymentManagement';

UPDATE `bp_menu`
SET `meta` = JSON_SET(
  COALESCE(`meta`, JSON_OBJECT()),
  '$.title', 'navigation.user.packagesAssets',
  '$.order', 3,
  '$.audience', 'user'
)
WHERE `id` = 84 AND `name` = 'PackageMall';

UPDATE `bp_menu`
SET `meta` = JSON_SET(
  COALESCE(`meta`, JSON_OBJECT()),
  '$.title', 'navigation.admin.riskMonitoring',
  '$.order', 5,
  '$.audience', 'admin'
)
WHERE `id` = 90 AND `name` = 'DashboardStats';

UPDATE `bp_menu`
SET `meta` = JSON_SET(
  COALESCE(`meta`, JSON_OBJECT()),
  '$.order', 7,
  '$.audience', 'admin'
)
WHERE `id` = 120 AND `name` = 'ContentOperations';

UPDATE `bp_menu`
SET `meta` = JSON_SET(
  COALESCE(`meta`, JSON_OBJECT()),
  '$.order', 8,
  '$.audience', 'admin'
)
WHERE `id` = 30 AND `name` = 'System';

INSERT INTO `bp_menu`
  (`id`, `pid`, `name`, `path`, `component`, `redirect`, `type`, `auth_code`, `meta`, `status`)
VALUES
  (210, 0, 'UserServiceSupport', '/console/support', NULL, '/console/tickets', 'catalog', NULL, JSON_OBJECT('title', 'navigation.user.serviceSupport', 'icon', 'lucide:life-buoy', 'order', 4, 'audience', 'user'), 1),
  (202, 210, 'UserNoticeCenter', '/notice', '/notice/user/list', NULL, 'menu', NULL, JSON_OBJECT('title', 'system.notice.userTitle', 'icon', 'mdi:bullhorn-outline', 'order', 2, 'audience', 'user'), 1),
  (203, 0, 'Profile', '/profile', '/_core/profile/index', NULL, 'menu', NULL, JSON_OBJECT('title', 'page.auth.profile', 'icon', 'lucide:user-cog', 'order', 5, 'audience', 'user', 'hideInMenu', false), 1),
  (204, 0, 'AdminTicketManagement', '/admin/tickets', '/ticket/admin', NULL, 'menu', 'ticket:admin:list', JSON_OBJECT('title', 'ticket.adminTitle', 'icon', 'lucide:message-square-reply', 'order', 6, 'audience', 'admin'), 1)
ON DUPLICATE KEY UPDATE
  `pid` = VALUES(`pid`),
  `path` = VALUES(`path`),
  `component` = VALUES(`component`),
  `redirect` = VALUES(`redirect`),
  `type` = VALUES(`type`),
  `auth_code` = VALUES(`auth_code`),
  `meta` = VALUES(`meta`),
  `status` = VALUES(`status`);

UPDATE `bp_menu`
SET `pid` = 210,
    `name` = 'UserTicketCenter',
    `path` = '/console/tickets',
    `component` = '/ticket/user',
    `meta` = JSON_OBJECT('title', 'ticket.title', 'icon', 'lucide:messages-square', 'order', 1, 'audience', 'user')
WHERE `id` = 195;

UPDATE `bp_menu`
SET `pid` = 204
WHERE `id` BETWEEN 198 AND 200;

INSERT IGNORE INTO `bp_role_menu` (`role_id`, `menu_id`)
SELECT r.`id`, m.`id`
FROM `bp_role` r
JOIN `bp_menu` m ON m.`id` IN (195, 196, 197, 202, 203, 210)
WHERE r.`role_key` = 'user';

INSERT IGNORE INTO `bp_role_menu` (`role_id`, `menu_id`)
SELECT r.`id`, m.`id`
FROM `bp_role` r
JOIN `bp_menu` m ON m.`id` IN (198, 199, 200, 204)
WHERE r.`role_key` = 'admin';
