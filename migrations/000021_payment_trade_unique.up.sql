ALTER TABLE `bp_payment_order`
  ADD UNIQUE KEY `uk_bp_payment_order_trade` (`alipay_trade_no`);

ALTER TABLE `bp_payment_order`
  DROP KEY `idx_bp_payment_order_trade`;
