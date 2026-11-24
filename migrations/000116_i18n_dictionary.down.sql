DELETE FROM `bp_role_menu` WHERE `menu_id` IN (250, 251);
DELETE FROM `bp_menu` WHERE `id` IN (250, 251);
DROP TABLE IF EXISTS `bp_i18n_entry`;
DROP TABLE IF EXISTS `bp_i18n_locale`;
