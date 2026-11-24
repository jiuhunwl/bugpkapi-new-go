DELETE FROM `bp_role_menu` WHERE `menu_id` = 900112;
DELETE FROM `bp_menu` WHERE `id` = 900112;
ALTER TABLE `bp_payment_order` DROP COLUMN `deleted_at`;
