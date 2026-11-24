ALTER TABLE `bp_interface_call_log`
  ADD KEY `idx_bp_call_log_interface_success_created_id` (
    `interface_id`,
    `success`,
    `created_at`,
    `id`
  );
