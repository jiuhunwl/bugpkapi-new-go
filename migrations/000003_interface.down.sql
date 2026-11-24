DELETE FROM `bp_role_menu` WHERE `menu_id` BETWEEN 40 AND 50;
DELETE FROM `bp_menu` WHERE `id` BETWEEN 40 AND 50;

DROP TABLE IF EXISTS `bp_interface_call_log`;
DROP TABLE IF EXISTS `bp_interface_billing_rule`;
DROP TABLE IF EXISTS `bp_interface_api`;
