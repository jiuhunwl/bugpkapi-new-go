-- Remove the persisted bounded-repair state without assuming a particular
-- column order.  The guards keep rollback usable after a partially applied
-- manual upgrade.

SET @bugpk_analytics_repair_schema = DATABASE();

SET @bugpk_analytics_repair_sql = IF(EXISTS(
  SELECT 1 FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_analytics_repair_schema
    AND TABLE_NAME = 'bp_analytics_checkpoint'
    AND COLUMN_NAME = 'repair_completed_at'
), 'ALTER TABLE `bp_analytics_checkpoint` DROP COLUMN `repair_completed_at`', 'SELECT 1 WHERE 0');
PREPARE bugpk_analytics_repair_stmt FROM @bugpk_analytics_repair_sql;
EXECUTE bugpk_analytics_repair_stmt;
DEALLOCATE PREPARE bugpk_analytics_repair_stmt;

SET @bugpk_analytics_repair_sql = IF(EXISTS(
  SELECT 1 FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_analytics_repair_schema
    AND TABLE_NAME = 'bp_analytics_checkpoint'
    AND COLUMN_NAME = 'repair_started_at'
), 'ALTER TABLE `bp_analytics_checkpoint` DROP COLUMN `repair_started_at`', 'SELECT 1 WHERE 0');
PREPARE bugpk_analytics_repair_stmt FROM @bugpk_analytics_repair_sql;
EXECUTE bugpk_analytics_repair_stmt;
DEALLOCATE PREPARE bugpk_analytics_repair_stmt;

SET @bugpk_analytics_repair_sql = IF(EXISTS(
  SELECT 1 FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_analytics_repair_schema
    AND TABLE_NAME = 'bp_analytics_checkpoint'
    AND COLUMN_NAME = 'repair_requested_at'
), 'ALTER TABLE `bp_analytics_checkpoint` DROP COLUMN `repair_requested_at`', 'SELECT 1 WHERE 0');
PREPARE bugpk_analytics_repair_stmt FROM @bugpk_analytics_repair_sql;
EXECUTE bugpk_analytics_repair_stmt;
DEALLOCATE PREPARE bugpk_analytics_repair_stmt;

SET @bugpk_analytics_repair_sql = IF(EXISTS(
  SELECT 1 FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_analytics_repair_schema
    AND TABLE_NAME = 'bp_analytics_checkpoint'
    AND COLUMN_NAME = 'repair_window_days'
), 'ALTER TABLE `bp_analytics_checkpoint` DROP COLUMN `repair_window_days`', 'SELECT 1 WHERE 0');
PREPARE bugpk_analytics_repair_stmt FROM @bugpk_analytics_repair_sql;
EXECUTE bugpk_analytics_repair_stmt;
DEALLOCATE PREPARE bugpk_analytics_repair_stmt;

SET @bugpk_analytics_repair_sql = IF(EXISTS(
  SELECT 1 FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_analytics_repair_schema
    AND TABLE_NAME = 'bp_analytics_checkpoint'
    AND COLUMN_NAME = 'repair_status'
), 'ALTER TABLE `bp_analytics_checkpoint` DROP COLUMN `repair_status`', 'SELECT 1 WHERE 0');
PREPARE bugpk_analytics_repair_stmt FROM @bugpk_analytics_repair_sql;
EXECUTE bugpk_analytics_repair_stmt;
DEALLOCATE PREPARE bugpk_analytics_repair_stmt;

