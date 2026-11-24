-- Day-grain rollups are the read layer for the 7-day trend, the active-user
-- ranking and the console trend/hot-API widgets. Those reads previously walked
-- the minute projections, so a single user-day cost 1,440 rows and a 7-day
-- window scanned days x users x 1,440 even though only one number per day is
-- ever displayed.
--
-- bp_call_metric_day has no user dimension at all, so it stays at one row per
-- day forever; bp_call_user_metric_day keeps the user dimension because the
-- account-status filter (bp_user join) is applied at read time and must remain
-- exact rather than being frozen into the aggregate.
--
-- Both tables are fed from the append-only call log by the rollup worker using
-- the same eligibility predicate as bp_call_metric_minute, so their numbers are
-- the same arithmetic over a coarser bucket. The worker aggregates one batch at
-- a time, therefore ON DUPLICATE KEY UPDATE must accumulate
-- (col=col+VALUES(col)); a replayed batch is only ever replayed when every
-- projection is empty (see rollupState.needsReplay), so accumulation cannot
-- double count.

CREATE TABLE IF NOT EXISTS `bp_call_metric_day` (
  `bucket_date` date NOT NULL,
  `call_count` bigint unsigned NOT NULL DEFAULT 0,
  `success_count` bigint unsigned NOT NULL DEFAULT 0,
  `failure_count` bigint unsigned NOT NULL DEFAULT 0,
  `billable_count` bigint unsigned NOT NULL DEFAULT 0,
  `charge_amount` decimal(38,4) NOT NULL DEFAULT 0.0000,
  `point_amount` decimal(38,0) NOT NULL DEFAULT 0,
  `request_bytes` decimal(38,0) NOT NULL DEFAULT 0,
  `response_bytes` decimal(38,0) NOT NULL DEFAULT 0,
  `elapsed_sum` decimal(38,0) NOT NULL DEFAULT 0,
  `elapsed_min` bigint unsigned NOT NULL DEFAULT 0,
  `elapsed_max` bigint unsigned NOT NULL DEFAULT 0,
  `first_seen` datetime NOT NULL,
  `last_seen` datetime NOT NULL,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`bucket_date`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='All-user call metrics by day; 1 row/day for dashboard trends';

CREATE TABLE IF NOT EXISTS `bp_call_user_metric_day` (
  `bucket_date` date NOT NULL,
  `user_id` bigint NOT NULL,
  `call_count` bigint unsigned NOT NULL DEFAULT 0,
  `success_count` bigint unsigned NOT NULL DEFAULT 0,
  `failure_count` bigint unsigned NOT NULL DEFAULT 0,
  `billable_count` bigint unsigned NOT NULL DEFAULT 0,
  `charge_amount` decimal(38,4) NOT NULL DEFAULT 0.0000,
  `point_amount` decimal(38,0) NOT NULL DEFAULT 0,
  `request_bytes` decimal(38,0) NOT NULL DEFAULT 0,
  `response_bytes` decimal(38,0) NOT NULL DEFAULT 0,
  `elapsed_sum` decimal(38,0) NOT NULL DEFAULT 0,
  `elapsed_min` bigint unsigned NOT NULL DEFAULT 0,
  `elapsed_max` bigint unsigned NOT NULL DEFAULT 0,
  `first_seen` datetime NOT NULL,
  `last_seen` datetime NOT NULL,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`bucket_date`,`user_id`),
  KEY `idx_bp_call_user_metric_day_user_time` (`user_id`,`bucket_date`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='Per-user call metrics by day; 1 row/user/day for active-user rankings';

-- Backfill from the already-aggregated minute projection instead of the raw
-- call log so an upgrade never re-scans append-only traffic. The window is
-- bounded to 180 days for the same reason the hour layer uses 180 days: wider
-- history is not read by any dashboard widget, and an unbounded GROUP BY over
-- the full minute table would be the longest statement of the migration.
INSERT INTO `bp_call_metric_day` (
  `bucket_date`,`call_count`,`success_count`,`failure_count`,`billable_count`,
  `charge_amount`,`point_amount`,`request_bytes`,`response_bytes`,`elapsed_sum`,
  `elapsed_min`,`elapsed_max`,`first_seen`,`last_seen`
)
SELECT
  DATE(`bucket_start`),SUM(`call_count`),SUM(`success_count`),SUM(`failure_count`),
  SUM(`billable_count`),SUM(`charge_amount`),SUM(`point_amount`),SUM(`request_bytes`),
  SUM(`response_bytes`),SUM(`elapsed_sum`),MIN(`elapsed_min`),MAX(`elapsed_max`),
  MIN(`first_seen`),MAX(`last_seen`)
FROM `bp_call_metric_minute` FORCE INDEX (`PRIMARY`)
WHERE `bucket_start` >= DATE_SUB(NOW(), INTERVAL 180 DAY)
GROUP BY DATE(`bucket_start`)
ON DUPLICATE KEY UPDATE
  `call_count`=VALUES(`call_count`),
  `success_count`=VALUES(`success_count`),
  `failure_count`=VALUES(`failure_count`),
  `billable_count`=VALUES(`billable_count`),
  `charge_amount`=VALUES(`charge_amount`),
  `point_amount`=VALUES(`point_amount`),
  `request_bytes`=VALUES(`request_bytes`),
  `response_bytes`=VALUES(`response_bytes`),
  `elapsed_sum`=VALUES(`elapsed_sum`),
  `elapsed_min`=VALUES(`elapsed_min`),
  `elapsed_max`=VALUES(`elapsed_max`),
  `first_seen`=VALUES(`first_seen`),
  `last_seen`=VALUES(`last_seen`);

INSERT INTO `bp_call_user_metric_day` (
  `bucket_date`,`user_id`,`call_count`,`success_count`,`failure_count`,`billable_count`,
  `charge_amount`,`point_amount`,`request_bytes`,`response_bytes`,`elapsed_sum`,
  `elapsed_min`,`elapsed_max`,`first_seen`,`last_seen`
)
SELECT
  DATE(`bucket_start`),`user_id`,SUM(`call_count`),SUM(`success_count`),SUM(`failure_count`),
  SUM(`billable_count`),SUM(`charge_amount`),SUM(`point_amount`),SUM(`request_bytes`),
  SUM(`response_bytes`),SUM(`elapsed_sum`),MIN(`elapsed_min`),MAX(`elapsed_max`),
  MIN(`first_seen`),MAX(`last_seen`)
FROM `bp_call_user_metric_minute` FORCE INDEX (`PRIMARY`)
WHERE `bucket_start` >= DATE_SUB(NOW(), INTERVAL 180 DAY)
GROUP BY DATE(`bucket_start`),`user_id`
ON DUPLICATE KEY UPDATE
  `call_count`=VALUES(`call_count`),
  `success_count`=VALUES(`success_count`),
  `failure_count`=VALUES(`failure_count`),
  `billable_count`=VALUES(`billable_count`),
  `charge_amount`=VALUES(`charge_amount`),
  `point_amount`=VALUES(`point_amount`),
  `request_bytes`=VALUES(`request_bytes`),
  `response_bytes`=VALUES(`response_bytes`),
  `elapsed_sum`=VALUES(`elapsed_sum`),
  `elapsed_min`=VALUES(`elapsed_min`),
  `elapsed_max`=VALUES(`elapsed_max`),
  `first_seen`=VALUES(`first_seen`),
  `last_seen`=VALUES(`last_seen`);
