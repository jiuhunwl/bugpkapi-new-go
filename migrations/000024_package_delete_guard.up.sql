ALTER TABLE `bp_user_package_global`
  ADD KEY `idx_bp_user_package_global_package` (`package_id`, `id`);

ALTER TABLE `bp_user_package_interface`
  ADD KEY `idx_bp_user_package_interface_package` (`package_id`, `spec_id`, `id`);

ALTER TABLE `bp_payment_order`
  ADD KEY `idx_bp_payment_order_target` (`order_type`, `target_id`, `status`, `id`);
