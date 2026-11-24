DELETE FROM `bp_role_menu`
WHERE `menu_id` IN (202, 203, 204, 210);

UPDATE `bp_menu`
SET `pid` = 195
WHERE `id` BETWEEN 198 AND 200;

UPDATE `bp_menu`
SET `pid` = 0,
    `name` = 'TicketCenter',
    `path` = '/tickets',
    `component` = '/ticket/index',
    `meta` = JSON_OBJECT('title', 'ticket.title', 'icon', 'lucide:messages-square', 'order', 9988)
WHERE `id` = 195;

DELETE FROM `bp_menu`
WHERE `id` IN (202, 203, 204, 210);

UPDATE `bp_menu`
SET `path` = '/interface/list'
WHERE `id` = 41 AND `name` = 'InterfaceList';

UPDATE `bp_menu`
SET `path` = '/interface/log'
WHERE `id` = 50 AND `name` = 'InterfaceCallLog';

UPDATE `bp_menu`
SET `meta` = JSON_SET(
  JSON_REMOVE(COALESCE(`meta`, JSON_OBJECT()), '$.adminTitle', '$.adminOrder', '$.userTitle', '$.userOrder'),
  '$.title', 'system.interface.title',
  '$.order', 9995
)
WHERE `id` = 40 AND `name` = 'InterfaceManagement';

UPDATE `bp_menu`
SET `pid` = 0,
    `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.order', 9994)
WHERE `id` = 60 AND `name` = 'ApiKeyManagement';

UPDATE `bp_menu`
SET `meta` = JSON_SET(
  JSON_REMOVE(COALESCE(`meta`, JSON_OBJECT()), '$.audience'),
  '$.title', 'system.user.title',
  '$.order', 9996
)
WHERE `id` = 10 AND `name` = 'UserManagement';

UPDATE `bp_menu`
SET `meta` = JSON_SET(
  JSON_REMOVE(COALESCE(`meta`, JSON_OBJECT()), '$.audience'),
  '$.title', 'system.package.title',
  '$.order', 9993
)
WHERE `id` = 70 AND `name` = 'PackageManagement';

UPDATE `bp_menu`
SET `pid` = 0,
    `meta` = JSON_SET(
      JSON_REMOVE(COALESCE(`meta`, JSON_OBJECT()), '$.audience'),
      '$.order', 9990
    )
WHERE `id` = 100 AND `name` = 'PaymentManagement';

UPDATE `bp_menu`
SET `meta` = JSON_SET(
  JSON_REMOVE(COALESCE(`meta`, JSON_OBJECT()), '$.audience'),
  '$.title', '套餐商城',
  '$.order', 9992
)
WHERE `id` = 84 AND `name` = 'PackageMall';

UPDATE `bp_menu`
SET `meta` = JSON_SET(
  JSON_REMOVE(COALESCE(`meta`, JSON_OBJECT()), '$.audience'),
  '$.title', '数据看板',
  '$.order', 9991
)
WHERE `id` = 90 AND `name` = 'DashboardStats';

UPDATE `bp_menu`
SET `meta` = JSON_SET(
  JSON_REMOVE(COALESCE(`meta`, JSON_OBJECT()), '$.audience'),
  '$.order', 9990
)
WHERE `id` = 120 AND `name` = 'ContentOperations';

UPDATE `bp_menu`
SET `meta` = JSON_SET(
  JSON_REMOVE(COALESCE(`meta`, JSON_OBJECT()), '$.audience'),
  '$.order', 9997
)
WHERE `id` = 30 AND `name` = 'System';
