-- Workspace is the single dashboard entry. HomeOverview points to the same
-- component and must not remain as a second navigation item.
DELETE FROM `bp_role_menu`
WHERE `menu_id` = 141;

DELETE FROM `bp_menu`
WHERE `id` = 141
   OR `name` = 'HomeOverview'
   OR `path` = '/dashboard/home-overview';
