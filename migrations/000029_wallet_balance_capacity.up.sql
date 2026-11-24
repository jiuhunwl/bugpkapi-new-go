ALTER TABLE `bp_user`
  MODIFY COLUMN `balance` decimal(18,4) NOT NULL DEFAULT 0.0000;

ALTER TABLE `bp_wallet_transaction`
  MODIFY COLUMN `amount` decimal(18,4) NOT NULL DEFAULT 0.0000,
  MODIFY COLUMN `balance_after` decimal(18,4) NOT NULL DEFAULT 0.0000;
