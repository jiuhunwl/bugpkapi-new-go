ALTER TABLE `bp_user_package_interface`
  DROP KEY `idx_bp_user_package_interface_primary`,
  DROP COLUMN `is_primary`;

ALTER TABLE `bp_user_package_global`
  DROP KEY `idx_bp_user_package_global_primary`,
  DROP COLUMN `is_primary`;
