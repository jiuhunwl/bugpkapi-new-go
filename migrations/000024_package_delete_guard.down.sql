ALTER TABLE `bp_payment_order`
  DROP KEY `idx_bp_payment_order_target`;

ALTER TABLE `bp_user_package_interface`
  DROP KEY `idx_bp_user_package_interface_package`;

ALTER TABLE `bp_user_package_global`
  DROP KEY `idx_bp_user_package_global_package`;
