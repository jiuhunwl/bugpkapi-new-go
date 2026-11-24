-- Dedicated permission for the one-click account ban action on the risk dashboard.
INSERT INTO `bp_menu`
  (`id`, `pid`, `name`, `path`, `component`, `redirect`, `type`, `auth_code`, `meta`, `status`)
VALUES
  (201, 170, 'RiskControlUserBan', '', NULL, NULL, 'button', 'risk-control:user:ban', JSON_OBJECT('title', 'system.riskControl.permissions.banUser', 'order', 2), 1)
ON DUPLICATE KEY UPDATE
  `pid` = VALUES(`pid`),
  `auth_code` = VALUES(`auth_code`),
  `meta` = VALUES(`meta`),
  `status` = VALUES(`status`);

INSERT IGNORE INTO `bp_role_menu` (`role_id`, `menu_id`)
SELECT 1, 201;
