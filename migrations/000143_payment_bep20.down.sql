ALTER TABLE `bp_payment_method` DROP CHECK `chk_bp_payment_method_code`;
ALTER TABLE `bp_payment_method`
  ADD CONSTRAINT `chk_bp_payment_method_code` CHECK (`method_code` IN ('BALANCE','ALIPAY','WECHAT','HUPIJIAO_ALIPAY','HUPIJIAO_WECHAT','TRC20'));
DELETE FROM `bp_payment_method` WHERE `method_code` = 'BEP20';

DROP TABLE IF EXISTS `bp_payment_bep20_transfer`;
DROP TABLE IF EXISTS `bp_payment_bep20_config`;
