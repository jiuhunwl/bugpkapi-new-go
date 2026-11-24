UPDATE `bp_menu`
SET `path` = '/console/workspace',
    `component` = '/dashboard/workspace/index',
    `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.title', 'page.dashboard.workspace', '$.affixTab', true, '$.keepAlive', true)
WHERE `name` = 'Workspace';

ALTER TABLE `bp_user`
  MODIFY COLUMN `home_path` varchar(128) NOT NULL DEFAULT '/console/workspace' COMMENT '登录后首页路径';

UPDATE `bp_user`
SET `home_path` = '/console/workspace'
WHERE `home_path` = '/workspace';
