INSERT INTO `bp_menu` (`id`,`pid`,`name`,`path`,`component`,`type`,`auth_code`,`meta`,`status`)
SELECT 61, 60, 'ApiKeyList', '/key/list', '/key/list', 'menu', 'key:list',
       JSON_OBJECT('title','system.key.listTitle','icon','lucide:list-key','order',1), 1
WHERE NOT EXISTS (SELECT 1 FROM `bp_menu` WHERE `id` = 61);

UPDATE `bp_menu`
SET `pid` = 60, `path` = '/key', `component` = NULL, `type` = 'catalog', `auth_code` = 'key:root'
WHERE `id` = 60 AND `name` = 'ApiKeyManagement';
