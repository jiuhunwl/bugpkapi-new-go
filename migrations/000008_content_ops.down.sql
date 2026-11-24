DELETE FROM `bp_role_menu` WHERE `menu_id` BETWEEN 120 AND 141;
DELETE FROM `bp_menu` WHERE `id` BETWEEN 120 AND 141;

DROP TABLE IF EXISTS `bp_friend_link_config`;
DROP TABLE IF EXISTS `bp_friend_link_application`;
DROP TABLE IF EXISTS `bp_friend_link`;
DROP TABLE IF EXISTS `bp_site_config`;
DROP TABLE IF EXISTS `bp_home_notice_config`;
DROP TABLE IF EXISTS `bp_notice`;
