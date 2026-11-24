-- 000090: persist a bounded analytics projection repair request.
--
-- Migrations 000078, 000079 and 000089 create/read rollup projections, but
-- they cannot tell whether an older installation already advanced its
-- checkpoint while only a subset of those projections was populated.  Mark
-- one bounded repair task here; the Go worker performs the actual rebuild
-- outside the migration transaction.  Keeping the migration DDL/data-only
-- makes upgrades safe for very large append-only call-log tables.

SET @bugpk_analytics_repair_schema = DATABASE();

SET @bugpk_analytics_repair_sql = IF(EXISTS(
  SELECT 1
  FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_analytics_repair_schema
    AND TABLE_NAME = 'bp_analytics_checkpoint'
    AND COLUMN_NAME = 'repair_status'
), 'SELECT 1 WHERE 0',
  'ALTER TABLE `bp_analytics_checkpoint` ADD COLUMN `repair_status` varchar(16) NOT NULL DEFAULT ''NONE'' COMMENT ''Bounded rollup repair state'' AFTER `last_error`');
PREPARE bugpk_analytics_repair_stmt FROM @bugpk_analytics_repair_sql;
EXECUTE bugpk_analytics_repair_stmt;
DEALLOCATE PREPARE bugpk_analytics_repair_stmt;

SET @bugpk_analytics_repair_sql = IF(EXISTS(
  SELECT 1
  FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_analytics_repair_schema
    AND TABLE_NAME = 'bp_analytics_checkpoint'
    AND COLUMN_NAME = 'repair_window_days'
), 'SELECT 1 WHERE 0',
  'ALTER TABLE `bp_analytics_checkpoint` ADD COLUMN `repair_window_days` smallint unsigned NOT NULL DEFAULT 0 COMMENT ''Bounded repair window in days'' AFTER `repair_status`');
PREPARE bugpk_analytics_repair_stmt FROM @bugpk_analytics_repair_sql;
EXECUTE bugpk_analytics_repair_stmt;
DEALLOCATE PREPARE bugpk_analytics_repair_stmt;

SET @bugpk_analytics_repair_sql = IF(EXISTS(
  SELECT 1
  FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_analytics_repair_schema
    AND TABLE_NAME = 'bp_analytics_checkpoint'
    AND COLUMN_NAME = 'repair_requested_at'
), 'SELECT 1 WHERE 0',
  'ALTER TABLE `bp_analytics_checkpoint` ADD COLUMN `repair_requested_at` datetime(6) DEFAULT NULL COMMENT ''Time the bounded repair was requested'' AFTER `repair_window_days`');
PREPARE bugpk_analytics_repair_stmt FROM @bugpk_analytics_repair_sql;
EXECUTE bugpk_analytics_repair_stmt;
DEALLOCATE PREPARE bugpk_analytics_repair_stmt;

SET @bugpk_analytics_repair_sql = IF(EXISTS(
  SELECT 1
  FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_analytics_repair_schema
    AND TABLE_NAME = 'bp_analytics_checkpoint'
    AND COLUMN_NAME = 'repair_started_at'
), 'SELECT 1 WHERE 0',
  'ALTER TABLE `bp_analytics_checkpoint` ADD COLUMN `repair_started_at` datetime(6) DEFAULT NULL COMMENT ''Time the bounded repair started'' AFTER `repair_requested_at`');
PREPARE bugpk_analytics_repair_stmt FROM @bugpk_analytics_repair_sql;
EXECUTE bugpk_analytics_repair_stmt;
DEALLOCATE PREPARE bugpk_analytics_repair_stmt;

SET @bugpk_analytics_repair_sql = IF(EXISTS(
  SELECT 1
  FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_analytics_repair_schema
    AND TABLE_NAME = 'bp_analytics_checkpoint'
    AND COLUMN_NAME = 'repair_completed_at'
), 'SELECT 1 WHERE 0',
  'ALTER TABLE `bp_analytics_checkpoint` ADD COLUMN `repair_completed_at` datetime(6) DEFAULT NULL COMMENT ''Time the bounded repair completed'' AFTER `repair_started_at`');
PREPARE bugpk_analytics_repair_stmt FROM @bugpk_analytics_repair_sql;
EXECUTE bugpk_analytics_repair_stmt;
DEALLOCATE PREPARE bugpk_analytics_repair_stmt;

INSERT IGNORE INTO `bp_analytics_checkpoint` (`worker_key`)
VALUES ('call_log_rollup');

-- Request the repair exactly once for this migration.  A completed request is
-- preserved if an operator re-runs the standalone upgrade SQL; pending/running
-- requests remain retryable and never reset the source checkpoint.
-- The worker interprets the default as a bounded `INTERVAL 31 DAY` window.
UPDATE `bp_analytics_checkpoint`
SET `repair_status` = IF(`repair_status` = 'COMPLETE', 'COMPLETE', 'PENDING'),
    `repair_window_days` = IF(`repair_window_days` = 0, 31, `repair_window_days`),
    `repair_requested_at` = IF(
      `repair_status` = 'COMPLETE',
      `repair_requested_at`,
      COALESCE(`repair_requested_at`, NOW(6))
    ),
    `repair_started_at` = IF(`repair_status` = 'COMPLETE', `repair_started_at`, NULL),
    `repair_completed_at` = IF(`repair_status` = 'COMPLETE', `repair_completed_at`, NULL)
WHERE `worker_key` = 'call_log_rollup';
