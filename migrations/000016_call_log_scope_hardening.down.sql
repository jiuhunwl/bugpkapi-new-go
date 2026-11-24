INSERT IGNORE INTO `bp_role_menu` (`role_id`, `menu_id`)
SELECT 2, `id`
FROM `bp_menu`
WHERE `id` IN (50, 93);
