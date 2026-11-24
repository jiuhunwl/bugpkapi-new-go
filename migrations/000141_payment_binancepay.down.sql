-- 000141: 币安 Pay 商户充值通道（覆盖「链下转账」）的回滚。
-- 仅移除本通道自有对象；订单表与其它通道不受影响。

DROP TABLE IF EXISTS `bp_payment_binancepay_nonce`;
DROP TABLE IF EXISTS `bp_payment_binancepay_config`;
