ALTER TABLE `bp_payment_order`
  ADD KEY `idx_bp_payment_order_paid` (`status`, `paid_at`);
