-- Canonicalize the shared self-service routes and repair permissions by role
-- key.  Do not depend on built-in role ids: upgraded installations may have
-- created the admin/user roles under different primary keys.

UPDATE `bp_menu`
SET `pid` = 0,
    `path` = '/key',
    `component` = '/key/list',
    `type` = 'menu',
    `auth_code` = 'key:list',
    `meta` = JSON_SET(
      COALESCE(`meta`, JSON_OBJECT()),
      '$.title', 'system.key.title',
      '$.adminTitle', 'system.key.title',
      '$.adminOrder', 3,
      '$.userTitle', 'system.key.myKeys',
      '$.userOrder', 3,
      '$.audience', 'all'
    ),
    `status` = 1
WHERE `id` = 60 AND `name` = 'ApiKeyManagement';

-- Some installations still contain the former catalog -> list pair.  Move
-- both role grants and action buttons to the promoted menu before removing it.
INSERT IGNORE INTO `bp_role_menu` (`role_id`, `menu_id`)
SELECT `role_id`, 60
FROM `bp_role_menu`
WHERE `menu_id` = 61
  AND EXISTS (
    SELECT 1 FROM `bp_menu`
    WHERE `id` = 60 AND `name` = 'ApiKeyManagement'
  );

UPDATE `bp_menu`
SET `pid` = 60
WHERE `pid` = 61;

DELETE FROM `bp_role_menu`
WHERE `menu_id` = 61;

DELETE FROM `bp_menu`
WHERE `id` = 61 AND `name` = 'ApiKeyList';

UPDATE `bp_menu`
SET `path` = '/console/interfaces',
    `component` = '/interface/list',
    `auth_code` = 'interface:list',
    `meta` = JSON_SET(
      COALESCE(`meta`, JSON_OBJECT()),
      '$.adminOrder', 1,
      '$.userOrder', 2,
      '$.audience', 'all'
    ),
    `status` = 1
WHERE `id` = 41 AND `name` = 'InterfaceList';

UPDATE `bp_menu`
SET `path` = '/console/call-logs',
    `component` = '/interface/log',
    `auth_code` = 'log:call:list',
    `meta` = JSON_SET(
      COALESCE(`meta`, JSON_OBJECT()),
      '$.adminOrder', 2,
      '$.userOrder', 2.1,
      '$.audience', 'all'
    ),
    `status` = 1
WHERE `id` = 50 AND `name` = 'InterfaceCallLog';

-- The management catalog is administrator-only.  User navigation receives
-- the two canonical self-service routes above as direct top-level entries.
UPDATE `bp_menu`
SET `meta` = JSON_SET(
      COALESCE(`meta`, JSON_OBJECT()),
      '$.audience', 'admin',
      '$.navigationCleanup', '000077'
    )
WHERE `id` = 40 AND `name` = 'InterfaceManagement';

-- Disable obsolete routes without deleting them.  A marker makes rollback
-- target only rows changed by this migration.
UPDATE `bp_menu`
SET `status` = 0,
    `meta` = JSON_SET(
      COALESCE(`meta`, JSON_OBJECT()),
      '$.hideInMenu', true,
      '$.navigationCleanup', '000077-legacy-route'
    )
WHERE `status` = 1
  AND (
    `path` = '/console/apis'
    OR (
      `id` <> 41
      AND `path` IN ('/console/interfaces', '/interface/list')
    )
    OR (
      `id` <> 50
      AND `path` IN ('/console/call-logs', '/interface/log')
    )
  );

UPDATE `bp_menu`
SET `pid` = 0,
    `path` = '/system/about',
    `component` = '/system/about/index',
    `type` = 'menu',
    `auth_code` = 'system:about:view',
    `meta` = JSON_SET(
      COALESCE(`meta`, JSON_OBJECT()),
      '$.title', 'system.about.title',
      '$.order', 2147483647,
      '$.audience', 'all',
      '$.hideInMenu', false
    ),
    `status` = 1
WHERE `id` = 178 OR `name` = 'SystemAbout' OR `path` = '/system/about';

-- Ordinary users receive the API catalogue and masked call-log routes from
-- the scoped menu injector.  They must not inherit the administrator list
-- permissions from the historical shared rows, otherwise the frontend will
-- select management endpoints instead of /market and /user/call-logs.
DELETE rm
FROM `bp_role_menu` rm
JOIN `bp_role` r ON r.`id` = rm.`role_id`
WHERE r.`role_key` = 'user'
  AND rm.`menu_id` IN (40, 41, 50);

INSERT IGNORE INTO `bp_role_menu` (`role_id`, `menu_id`)
SELECT r.`id`, m.`id`
FROM `bp_role` r
JOIN `bp_menu` m ON m.`id` IN (40, 41, 50, 60, 62, 63, 64, 65, 66, 67, 178)
WHERE r.`role_key` = 'admin';

INSERT IGNORE INTO `bp_role_menu` (`role_id`, `menu_id`)
SELECT r.`id`, m.`id`
FROM `bp_role` r
JOIN `bp_menu` m ON m.`id` IN (60, 62, 63, 64, 65, 66, 67, 178)
WHERE r.`role_key` = 'user';
