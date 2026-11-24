-- Roll back only the resumable cursor metadata. Migration 000090 repair state
-- remains intact for compatibility with the previous worker implementation.

SET @bugpk_analytics_cursor_schema = DATABASE();

SET @bugpk_analytics_cursor_sql = IF(EXISTS(
  SELECT 1 FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_analytics_cursor_schema
    AND TABLE_NAME = 'bp_analytics_checkpoint'
    AND COLUMN_NAME = 'repair_clear_projection'
), 'ALTER TABLE `bp_analytics_checkpoint` DROP COLUMN `repair_clear_projection`', 'SELECT 1 WHERE 0');
PREPARE bugpk_analytics_cursor_stmt FROM @bugpk_analytics_cursor_sql;
EXECUTE bugpk_analytics_cursor_stmt;
DEALLOCATE PREPARE bugpk_analytics_cursor_stmt;

SET @bugpk_analytics_cursor_sql = IF(EXISTS(
  SELECT 1 FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_analytics_cursor_schema
    AND TABLE_NAME = 'bp_analytics_checkpoint'
    AND COLUMN_NAME = 'repair_window_start'
), 'ALTER TABLE `bp_analytics_checkpoint` DROP COLUMN `repair_window_start`', 'SELECT 1 WHERE 0');
PREPARE bugpk_analytics_cursor_stmt FROM @bugpk_analytics_cursor_sql;
EXECUTE bugpk_analytics_cursor_stmt;
DEALLOCATE PREPARE bugpk_analytics_cursor_stmt;

SET @bugpk_analytics_cursor_sql = IF(EXISTS(
  SELECT 1 FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_analytics_cursor_schema
    AND TABLE_NAME = 'bp_analytics_checkpoint'
    AND COLUMN_NAME = 'repair_phase'
), 'ALTER TABLE `bp_analytics_checkpoint` DROP COLUMN `repair_phase`', 'SELECT 1 WHERE 0');
PREPARE bugpk_analytics_cursor_stmt FROM @bugpk_analytics_cursor_sql;
EXECUTE bugpk_analytics_cursor_stmt;
DEALLOCATE PREPARE bugpk_analytics_cursor_stmt;

SET @bugpk_analytics_cursor_sql = IF(EXISTS(
  SELECT 1 FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_analytics_cursor_schema
    AND TABLE_NAME = 'bp_analytics_checkpoint'
    AND COLUMN_NAME = 'repair_projection_mask'
), 'ALTER TABLE `bp_analytics_checkpoint` DROP COLUMN `repair_projection_mask`', 'SELECT 1 WHERE 0');
PREPARE bugpk_analytics_cursor_stmt FROM @bugpk_analytics_cursor_sql;
EXECUTE bugpk_analytics_cursor_stmt;
DEALLOCATE PREPARE bugpk_analytics_cursor_stmt;

SET @bugpk_analytics_cursor_sql = IF(EXISTS(
  SELECT 1 FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_analytics_cursor_schema
    AND TABLE_NAME = 'bp_analytics_checkpoint'
    AND COLUMN_NAME = 'repair_cursor_id'
), 'ALTER TABLE `bp_analytics_checkpoint` DROP COLUMN `repair_cursor_id`', 'SELECT 1 WHERE 0');
PREPARE bugpk_analytics_cursor_stmt FROM @bugpk_analytics_cursor_sql;
EXECUTE bugpk_analytics_cursor_stmt;
DEALLOCATE PREPARE bugpk_analytics_cursor_stmt;

SET @bugpk_analytics_cursor_sql = IF(EXISTS(
  SELECT 1 FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_analytics_cursor_schema
    AND TABLE_NAME = 'bp_analytics_checkpoint'
    AND COLUMN_NAME = 'repair_upper_log_id'
), 'ALTER TABLE `bp_analytics_checkpoint` DROP COLUMN `repair_upper_log_id`', 'SELECT 1 WHERE 0');
PREPARE bugpk_analytics_cursor_stmt FROM @bugpk_analytics_cursor_sql;
EXECUTE bugpk_analytics_cursor_stmt;
DEALLOCATE PREPARE bugpk_analytics_cursor_stmt;
