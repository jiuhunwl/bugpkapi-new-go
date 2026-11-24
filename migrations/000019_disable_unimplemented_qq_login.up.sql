UPDATE `bp_auth_qq_login_config`
SET `enabled` = 0
WHERE `id` = 1;

UPDATE `bp_menu`
SET `status` = 0
WHERE `id` IN (158, 159);
