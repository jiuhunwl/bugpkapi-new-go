DELETE FROM `bp_role_menu` WHERE `menu_id` BETWEEN 100 AND 109;
DELETE FROM `bp_menu` WHERE `id` BETWEEN 100 AND 109;

DROP TABLE IF EXISTS `bp_payment_notify_log`;
DROP TABLE IF EXISTS `bp_payment_order`;
DROP TABLE IF EXISTS `bp_recharge_preset`;
DROP TABLE IF EXISTS `bp_payment_alipay_config`;
