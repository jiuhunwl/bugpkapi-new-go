ALTER TABLE `bp_payment_method` DROP CHECK `chk_bp_payment_method_code`;
ALTER TABLE `bp_payment_method`
  ADD CONSTRAINT `chk_bp_payment_method_code` CHECK (`method_code` IN ('BALANCE','ALIPAY','WECHAT','HUPIJIAO_ALIPAY','HUPIJIAO_WECHAT'));
DELETE FROM `bp_payment_method` WHERE `method_code` = 'TRC20';

ALTER TABLE `bp_payment_order`
  DROP INDEX `uk_bp_payment_order_chain_txid`,
  DROP INDEX `idx_bp_payment_order_chain_match`,
  DROP COLUMN `chain_confirm_count`,
  DROP COLUMN `chain_txid`,
  DROP COLUMN `chain_amount`,
  DROP COLUMN `chain_address`;

DROP TABLE IF EXISTS `bp_payment_trc20_transfer`;
DROP TABLE IF EXISTS `bp_payment_trc20_config`;
