UPDATE `bp_menu`
SET `auth_code` = 'log:call:list'
WHERE `id` = 50 AND `name` = 'InterfaceCallLog';

INSERT IGNORE INTO `bp_role_menu` (`role_id`, `menu_id`)
SELECT `role_id`, 50
FROM `bp_role_menu`
WHERE `menu_id` = 96;

DELETE FROM `bp_role_menu` WHERE `menu_id` = 96;
DELETE FROM `bp_menu` WHERE `id` = 96 AND `name` = 'CallLogListPermission';
