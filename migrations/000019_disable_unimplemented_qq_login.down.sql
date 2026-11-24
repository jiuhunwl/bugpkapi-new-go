UPDATE `bp_menu`
SET `status` = 1
WHERE `id` IN (158, 159);

INSERT IGNORE INTO `bp_role_menu` (`role_id`, `menu_id`)
SELECT 1, `id`
FROM `bp_menu`
WHERE `id` IN (158, 159);
