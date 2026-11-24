UPDATE `bp_menu`
SET `name` = 'SystemQuickLoginConfig',
    `auth_code` = 'system:login:qq-config',
    `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.title', '快捷登录设置', '$.icon', 'mdi:login-variant'),
    `status` = 0
WHERE `id` = 158;

UPDATE `bp_menu`
SET `name` = 'SystemQuickLoginConfigUpdate',
    `auth_code` = 'system:login:qq-config:update',
    `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.title', '保存快捷登录设置'),
    `status` = 0
WHERE `id` = 159;

ALTER TABLE `bp_user`
  DROP COLUMN `password_set`;

DROP TABLE IF EXISTS `bp_auth_oauth_identity`;
DROP TABLE IF EXISTS `bp_auth_oauth_provider`;
DROP TABLE IF EXISTS `bp_auth_oauth_config`;
