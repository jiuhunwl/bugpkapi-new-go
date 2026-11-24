DELETE FROM `bp_role_menu` WHERE `menu_id`=188;
DELETE FROM `bp_menu` WHERE `id`=188;

ALTER TABLE `bp_interface_api`
  DROP INDEX `idx_bp_interface_request_validation`,
  DROP COLUMN `request_validation_rules`,
  DROP COLUMN `request_validation_default_result`,
  DROP COLUMN `request_validation_enabled`;
