UPDATE `bp_menu`
SET `pid` = 0,
    `path` = '/key',
    `component` = '/key/list',
    `type` = 'menu',
    `auth_code` = 'key:list',
    `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.title', 'system.key.title')
WHERE `id` = 60 AND `name` = 'ApiKeyManagement';

INSERT IGNORE INTO `bp_role_menu` (`role_id`, `menu_id`)
SELECT `role_id`, 60
FROM `bp_role_menu`
WHERE `menu_id` = 61;

UPDATE `bp_menu`
SET `pid` = 60
WHERE `pid` = 61;

DELETE FROM `bp_role_menu`
WHERE `menu_id` = 61;

DELETE FROM `bp_menu`
WHERE `id` = 61 AND `name` = 'ApiKeyList';
