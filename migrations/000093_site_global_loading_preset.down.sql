-- 000093 (down): drop the global loading animation preset column.
--
-- The splash preset column is left untouched: it predates this migration and
-- rolling it back would discard an administrator's startup animation choice.

SET @bugpk_global_loading_schema = DATABASE();

SET @bugpk_global_loading_sql = IF(EXISTS(
  SELECT 1 FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_global_loading_schema
    AND TABLE_NAME = 'bp_site_config'
    AND COLUMN_NAME = 'global_loading_preset'
), 'ALTER TABLE `bp_site_config` DROP COLUMN `global_loading_preset`',
   'SELECT 1 WHERE 0');
PREPARE bugpk_global_loading_stmt FROM @bugpk_global_loading_sql;
EXECUTE bugpk_global_loading_stmt;
DEALLOCATE PREPARE bugpk_global_loading_stmt;
