-- 000092: make analytics projection repair resumable and transaction-bounded.
--
-- This migration only persists repair metadata. The serial Go analytics
-- worker clears and rebuilds the bounded window in small committed steps, so
-- a large installation can resume after a timeout or process restart without
-- repeating already committed source chunks.

SET @bugpk_analytics_cursor_schema = DATABASE();

SET @bugpk_analytics_cursor_sql = IF(EXISTS(
  SELECT 1 FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_analytics_cursor_schema
    AND TABLE_NAME = 'bp_analytics_checkpoint'
    AND COLUMN_NAME = 'repair_upper_log_id'
), 'SELECT 1 WHERE 0',
  'ALTER TABLE `bp_analytics_checkpoint` ADD COLUMN `repair_upper_log_id` bigint unsigned NOT NULL DEFAULT 0 COMMENT ''Stable source high-water mark for resumable repair'' AFTER `repair_completed_at`');
PREPARE bugpk_analytics_cursor_stmt FROM @bugpk_analytics_cursor_sql;
EXECUTE bugpk_analytics_cursor_stmt;
DEALLOCATE PREPARE bugpk_analytics_cursor_stmt;

SET @bugpk_analytics_cursor_sql = IF(EXISTS(
  SELECT 1 FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_analytics_cursor_schema
    AND TABLE_NAME = 'bp_analytics_checkpoint'
    AND COLUMN_NAME = 'repair_cursor_id'
), 'SELECT 1 WHERE 0',
  'ALTER TABLE `bp_analytics_checkpoint` ADD COLUMN `repair_cursor_id` bigint unsigned NOT NULL DEFAULT 0 COMMENT ''Last source id committed by resumable repair'' AFTER `repair_upper_log_id`');
PREPARE bugpk_analytics_cursor_stmt FROM @bugpk_analytics_cursor_sql;
EXECUTE bugpk_analytics_cursor_stmt;
DEALLOCATE PREPARE bugpk_analytics_cursor_stmt;

SET @bugpk_analytics_cursor_sql = IF(EXISTS(
  SELECT 1 FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_analytics_cursor_schema
    AND TABLE_NAME = 'bp_analytics_checkpoint'
    AND COLUMN_NAME = 'repair_projection_mask'
), 'SELECT 1 WHERE 0',
  'ALTER TABLE `bp_analytics_checkpoint` ADD COLUMN `repair_projection_mask` smallint unsigned NOT NULL DEFAULT 0 COMMENT ''Bit mask of projections rebuilt by current repair'' AFTER `repair_cursor_id`');
PREPARE bugpk_analytics_cursor_stmt FROM @bugpk_analytics_cursor_sql;
EXECUTE bugpk_analytics_cursor_stmt;
DEALLOCATE PREPARE bugpk_analytics_cursor_stmt;

SET @bugpk_analytics_cursor_sql = IF(EXISTS(
  SELECT 1 FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_analytics_cursor_schema
    AND TABLE_NAME = 'bp_analytics_checkpoint'
    AND COLUMN_NAME = 'repair_phase'
), 'SELECT 1 WHERE 0',
  'ALTER TABLE `bp_analytics_checkpoint` ADD COLUMN `repair_phase` varchar(16) NOT NULL DEFAULT ''IDLE'' COMMENT ''IDLE/CLEAR/ROLLUP/FINALIZE'' AFTER `repair_projection_mask`');
PREPARE bugpk_analytics_cursor_stmt FROM @bugpk_analytics_cursor_sql;
EXECUTE bugpk_analytics_cursor_stmt;
DEALLOCATE PREPARE bugpk_analytics_cursor_stmt;

SET @bugpk_analytics_cursor_sql = IF(EXISTS(
  SELECT 1 FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_analytics_cursor_schema
    AND TABLE_NAME = 'bp_analytics_checkpoint'
    AND COLUMN_NAME = 'repair_window_start'
), 'SELECT 1 WHERE 0',
  'ALTER TABLE `bp_analytics_checkpoint` ADD COLUMN `repair_window_start` datetime(6) DEFAULT NULL COMMENT ''Stable inclusive time boundary for current repair'' AFTER `repair_phase`');
PREPARE bugpk_analytics_cursor_stmt FROM @bugpk_analytics_cursor_sql;
EXECUTE bugpk_analytics_cursor_stmt;
DEALLOCATE PREPARE bugpk_analytics_cursor_stmt;

SET @bugpk_analytics_cursor_sql = IF(EXISTS(
  SELECT 1 FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_analytics_cursor_schema
    AND TABLE_NAME = 'bp_analytics_checkpoint'
    AND COLUMN_NAME = 'repair_clear_projection'
), 'SELECT 1 WHERE 0',
  'ALTER TABLE `bp_analytics_checkpoint` ADD COLUMN `repair_clear_projection` tinyint unsigned NOT NULL DEFAULT 0 COMMENT ''Next projection index to clear in bounded batches'' AFTER `repair_window_start`');
PREPARE bugpk_analytics_cursor_stmt FROM @bugpk_analytics_cursor_sql;
EXECUTE bugpk_analytics_cursor_stmt;
DEALLOCATE PREPARE bugpk_analytics_cursor_stmt;

INSERT IGNORE INTO `bp_analytics_checkpoint` (`worker_key`)
VALUES ('call_log_rollup');

-- repair_projection_mask=0 is the one-time migration marker. Keeping the mask
-- after completion makes this standalone SQL safe to rerun without scheduling
-- another destructive clear/rebuild cycle.
UPDATE `bp_analytics_checkpoint`
SET `repair_status` = 'PENDING',
    `repair_window_days` = IF(`repair_window_days` = 0, 31, `repair_window_days`),
    `repair_requested_at` = NOW(6),
    `repair_started_at` = NULL,
    `repair_completed_at` = NULL,
    `repair_upper_log_id` = 0,
    `repair_cursor_id` = 0,
    `repair_projection_mask` = 511,
    `repair_phase` = 'CLEAR',
    `repair_window_start` = NULL,
    `repair_clear_projection` = 0
WHERE `worker_key` = 'call_log_rollup'
  AND `repair_projection_mask` = 0;
