DELETE FROM `bp_role_menu` WHERE `menu_id` = 900123;
DELETE FROM `bp_menu` WHERE `id` = 900123;

ALTER TABLE `bp_wallet_transaction`
  DROP KEY `idx_bp_wallet_transaction_biz_created`;
