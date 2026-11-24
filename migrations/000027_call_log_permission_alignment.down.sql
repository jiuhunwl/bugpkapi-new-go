UPDATE `bp_menu`
SET `auth_code` = 'interface:call-log:list'
WHERE `id` = 50 AND `name` = 'InterfaceCallLog';

INSERT INTO `bp_menu`
  (`id`, `pid`, `name`, `path`, `component`, `redirect`, `type`, `auth_code`, `meta`, `status`)
VALUES
  (96, 90, 'CallLogListPermission', '', NULL, NULL, 'button', 'log:call:list', JSON_OBJECT('title', '调用日志列表', 'order', 6), 1)
ON DUPLICATE KEY UPDATE
  `pid` = VALUES(`pid`),
  `type` = VALUES(`type`),
  `auth_code` = VALUES(`auth_code`),
  `meta` = VALUES(`meta`),
  `status` = VALUES(`status`);

INSERT IGNORE INTO `bp_role_menu` (`role_id`, `menu_id`) VALUES (1, 96);
