DELETE FROM `bp_role_menu` WHERE `menu_id` = 111;
DELETE FROM `bp_menu` WHERE `id` = 111;
ALTER TABLE `bp_payment_order`
  DROP COLUMN `channel_trade_no`,
  DROP COLUMN `prepay_id`,
  DROP COLUMN `trade_type`;
DROP TABLE IF EXISTS `bp_payment_wechat_config`;
