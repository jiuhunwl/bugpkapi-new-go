-- Restore only legacy routes explicitly disabled by 000077.  Canonical route
-- and permission repairs are intentionally retained because removing them
-- would reintroduce 403/duplicate-menu failures on upgraded installations.
UPDATE `bp_menu`
SET `status` = 1,
    `meta` = JSON_REMOVE(
      COALESCE(`meta`, JSON_OBJECT()),
      '$.hideInMenu',
      '$.navigationCleanup'
    )
WHERE JSON_UNQUOTE(JSON_EXTRACT(`meta`, '$.navigationCleanup')) = '000077-legacy-route';

UPDATE `bp_menu`
SET `meta` = JSON_REMOVE(
      COALESCE(`meta`, JSON_OBJECT()),
      '$.navigationCleanup'
    )
WHERE `id` = 40
  AND JSON_UNQUOTE(JSON_EXTRACT(`meta`, '$.navigationCleanup')) = '000077';

DELETE rm
FROM `bp_role_menu` rm
JOIN `bp_role` r ON r.`id` = rm.`role_id`
WHERE r.`role_key` = 'user'
  AND rm.`menu_id` = 178;
