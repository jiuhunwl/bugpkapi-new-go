DELETE rm
FROM `bp_role_menu` rm
INNER JOIN `bp_menu` m ON m.`id` = rm.`menu_id`
WHERE m.`id` IN (112, 113);

DELETE FROM `bp_menu` WHERE `id` IN (112, 113);

ALTER TABLE `bp_payment_order`
  DROP KEY `idx_bp_payment_order_channel_status`,
  DROP COLUMN `pay_channel`;

DROP TABLE IF EXISTS `bp_payment_method`;
DROP TABLE IF EXISTS `bp_payment_hupijiao_config`;
