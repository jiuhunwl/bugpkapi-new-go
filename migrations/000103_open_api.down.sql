-- The open API tables are self-contained and unreferenced by existing
-- objects, so rollback is a straight drop plus the two bp_user switch columns
-- and the menu seeds. Key rows hold no money and no traffic, so dropping them
-- loses only credentials, never assets.
DELETE FROM `bp_role_menu` WHERE `menu_id` BETWEEN 230 AND 235 OR `menu_id` = 240;
DELETE FROM `bp_menu` WHERE `id` BETWEEN 230 AND 235 OR `id` = 240;

DROP TABLE IF EXISTS `bp_open_api_key_ip_whitelist`;
DROP TABLE IF EXISTS `bp_open_api_key_scope`;
DROP TABLE IF EXISTS `bp_open_api_key`;
DROP TABLE IF EXISTS `bp_open_api_config`;

ALTER TABLE `bp_user`
  DROP COLUMN `open_api_admin_disabled`,
  DROP COLUMN `open_api_enabled`;
