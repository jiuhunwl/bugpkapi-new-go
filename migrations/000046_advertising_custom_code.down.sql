DELETE FROM `bp_role_menu` WHERE `role_id`=1 AND `menu_id` BETWEEN 181 AND 186;
DELETE FROM `bp_menu` WHERE `id` BETWEEN 181 AND 186;
DROP TABLE IF EXISTS `bp_custom_code_config`;
DROP TABLE IF EXISTS `bp_advertisement`;
