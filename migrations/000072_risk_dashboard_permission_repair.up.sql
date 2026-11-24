-- 000068 originally reused menu id 201 for the user support catalog, which
-- overwrote RiskControlUserBan from 000066. Move that catalog to id 210 and
-- restore all administrator-only risk dashboard permissions by role key.

INSERT INTO `bp_menu`
  (`id`, `pid`, `name`, `path`, `component`, `redirect`, `type`, `auth_code`, `meta`, `status`)
VALUES
  (210, 0, 'UserServiceSupport', '/console/support', NULL, '/console/tickets', 'catalog', NULL, JSON_OBJECT('title', 'navigation.user.serviceSupport', 'icon', 'lucide:life-buoy', 'order', 4, 'audience', 'user'), 1)
ON DUPLICATE KEY UPDATE
  `pid` = VALUES(`pid`),
  `name` = VALUES(`name`),
  `path` = VALUES(`path`),
  `component` = VALUES(`component`),
  `redirect` = VALUES(`redirect`),
  `type` = VALUES(`type`),
  `auth_code` = VALUES(`auth_code`),
  `meta` = VALUES(`meta`),
  `status` = VALUES(`status`);

UPDATE `bp_menu`
SET `pid` = 210
WHERE (`id` = 195 AND `name` IN ('TicketCenter', 'UserTicketCenter'))
   OR (`id` = 202 AND `name` = 'UserNoticeCenter');

DELETE FROM `bp_role_menu`
WHERE `menu_id` = 201;

DELETE FROM `bp_menu`
WHERE `id` = 201;

INSERT INTO `bp_menu`
  (`id`, `pid`, `name`, `path`, `component`, `redirect`, `type`, `auth_code`, `meta`, `status`)
VALUES
  (201, 170, 'RiskControlUserBan', '', NULL, NULL, 'button', 'risk-control:user:ban', JSON_OBJECT('title', 'system.riskControl.permissions.banUser', 'order', 2), 1)
ON DUPLICATE KEY UPDATE
  `pid` = VALUES(`pid`),
  `name` = VALUES(`name`),
  `path` = VALUES(`path`),
  `component` = VALUES(`component`),
  `redirect` = VALUES(`redirect`),
  `type` = VALUES(`type`),
  `auth_code` = VALUES(`auth_code`),
  `meta` = VALUES(`meta`),
  `status` = VALUES(`status`);

INSERT IGNORE INTO `bp_role_menu` (`role_id`, `menu_id`)
SELECT r.`id`, m.`id`
FROM `bp_role` r
JOIN `bp_menu` m ON m.`id` IN (170, 172, 201)
WHERE r.`role_key` = 'admin';

INSERT IGNORE INTO `bp_role_menu` (`role_id`, `menu_id`)
SELECT r.`id`, m.`id`
FROM `bp_role` r
JOIN `bp_menu` m ON m.`id` IN (195, 202, 210)
WHERE r.`role_key` = 'user';
