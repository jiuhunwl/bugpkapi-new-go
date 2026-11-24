ALTER TABLE `bp_wallet_transaction`
  ADD KEY `idx_bp_wallet_transaction_user_biz` (`user_id`, `biz_type`, `biz_id`);
