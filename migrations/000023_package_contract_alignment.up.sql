ALTER TABLE `bp_package_interface_spec`
  ADD COLUMN `remark` varchar(255) DEFAULT NULL COMMENT '规格备注' AFTER `status`,
  ADD COLUMN `sort_order` int NOT NULL DEFAULT 1 COMMENT '规格排序' AFTER `remark`;

ALTER TABLE `bp_package_interface_spec`
  DROP KEY `idx_bp_package_interface_spec_pkg`,
  ADD KEY `idx_bp_package_interface_spec_pkg` (`package_id`, `status`, `sort_order`, `price`, `id`);
