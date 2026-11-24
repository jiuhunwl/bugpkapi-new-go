-- Forward-only permission/data repair. Removing these assignments on rollback
-- would revoke permissions that may have existed before this migration.
SELECT 1;
