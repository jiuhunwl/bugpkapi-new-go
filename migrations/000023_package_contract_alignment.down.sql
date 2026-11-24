ALTER TABLE `bp_package_interface_spec`
  DROP KEY `idx_bp_package_interface_spec_pkg`,
  ADD KEY `idx_bp_package_interface_spec_pkg` (`package_id`, `status`, `price`),
  DROP COLUMN `sort_order`,
  DROP COLUMN `remark`;
