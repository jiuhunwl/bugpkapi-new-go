DELETE FROM `bp_role_menu` WHERE `menu_id` BETWEEN 60 AND 67;
DELETE FROM `bp_menu` WHERE `id` BETWEEN 60 AND 67;

ALTER TABLE `bp_interface_call_log`
  DROP KEY `idx_bp_interface_call_log_api_code`,
  DROP KEY `idx_bp_interface_call_log_key`,
  DROP COLUMN `billing_snapshot`,
  DROP COLUMN `billing_status`,
  DROP COLUMN `api_code`,
  DROP COLUMN `api_key_id`;

DROP TABLE IF EXISTS `bp_api_key_ip_whitelist`;
DROP TABLE IF EXISTS `bp_api_key_interface`;
DROP TABLE IF EXISTS `bp_api_key`;
