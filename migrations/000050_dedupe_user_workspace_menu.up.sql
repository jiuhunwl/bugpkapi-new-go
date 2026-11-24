-- Keep the user-facing workspace as the single workspace entry point.
-- UserDashboardStats renders the same /dashboard/workspace/index component
-- as Workspace, so disable the duplicate menu while retaining its row and
-- role bindings for backwards-compatible rollback.
UPDATE `bp_menu`
SET `status` = 0
WHERE `name` = 'UserDashboardStats'
  AND `path` = '/dashboard-stats/user';

