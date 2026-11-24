ALTER TABLE `bp_billing_record`
  ADD KEY `idx_bp_billing_record_reconcile` (`status`, `created_at`, `id`);
