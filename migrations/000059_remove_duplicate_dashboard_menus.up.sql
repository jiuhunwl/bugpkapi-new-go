-- The administrator workbench is the single management dashboard. Remove the
-- duplicate management entry and the legacy standalone earth-monitor entry;
-- the workbench retains the global/China 2D and 3D atlas, while the risk
-- control screen retains its real-time failure and event views.
DELETE FROM `bp_role_menu`
WHERE `menu_id` IN (91, 171, 173);

DELETE FROM `bp_menu`
WHERE `id` IN (91, 171, 173);
