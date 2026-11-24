ALTER TABLE `bp_payment_order`
  DROP COLUMN `origin_amount`,
  DROP COLUMN `currency`;

DROP TABLE IF EXISTS `bp_payment_currency_config`;
