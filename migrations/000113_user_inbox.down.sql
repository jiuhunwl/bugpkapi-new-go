DELETE FROM `bp_role_menu` WHERE `menu_id` IN (241, 242);
DELETE FROM `bp_menu` WHERE `id` IN (241, 242);
DROP TABLE IF EXISTS `bp_user_inbox`;
