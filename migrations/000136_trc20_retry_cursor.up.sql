ALTER TABLE bp_payment_trc20_config
  ADD COLUMN retry_cursor_id bigint NOT NULL DEFAULT 0 COMMENT 'Durable round-robin receipt retry cursor';
ALTER TABLE bp_payment_order
  ADD KEY idx_bp_payment_order_chain_window (pay_channel, chain_amount, created_at);
