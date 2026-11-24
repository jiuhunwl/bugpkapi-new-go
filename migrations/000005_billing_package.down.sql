DELETE FROM `bp_role_menu` WHERE `menu_id` BETWEEN 70 AND 89;
DELETE FROM `bp_menu` WHERE `id` BETWEEN 70 AND 89;

ALTER TABLE `bp_interface_call_log`
  DROP KEY `idx_bp_interface_call_log_billing_record`,
  DROP COLUMN `billing_record_id`;

DROP TABLE IF EXISTS `bp_wallet_transaction`;
DROP TABLE IF EXISTS `bp_billing_record`;
DROP TABLE IF EXISTS `bp_user_package_interface`;
DROP TABLE IF EXISTS `bp_user_package_global`;
DROP TABLE IF EXISTS `bp_package_point`;
DROP TABLE IF EXISTS `bp_package_interface_spec`;
DROP TABLE IF EXISTS `bp_package_interface`;
DROP TABLE IF EXISTS `bp_package_global`;
