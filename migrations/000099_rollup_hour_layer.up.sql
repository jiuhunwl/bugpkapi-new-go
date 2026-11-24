-- Hour-grain rollups decouple the 7/30-day dashboard windows from the
-- per-minute scan cost. The minute projections stay authoritative for the
-- near-real-time (<= 36h) curve; any wider window is served from these three
-- tables, cutting the scanned rows by up to 60x without changing the numbers.
--
-- The backfill is sourced from the already-aggregated minute projections, not
-- from the append-only call log, so an upgrade never re-scans raw traffic.
-- Every ON DUPLICATE KEY UPDATE overwrites (col=VALUES(col)) instead of adding
-- (col=col+VALUES(col)): the source window is recomputed exactly, so a re-run
-- replaces the same buckets rather than double counting them.

CREATE TABLE IF NOT EXISTS `bp_call_metric_hour` (
  `bucket_start` datetime NOT NULL,
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
  PRIMARY KEY (`bucket_start`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='All-user call metrics by hour; the 7/30-day dashboard read layer';

CREATE TABLE IF NOT EXISTS `bp_call_user_metric_hour` (
  `bucket_start` datetime NOT NULL,
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
  PRIMARY KEY (`bucket_start`, `user_id`),
  KEY `idx_bp_call_user_metric_hour_user_time` (`user_id`, `bucket_start`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='Authoritative per-user call metrics by hour for current-account-aware totals';

CREATE TABLE IF NOT EXISTS `bp_call_api_metric_hour` (
  `bucket_start` datetime NOT NULL,
  `interface_id` bigint NOT NULL,
  `user_id` bigint NOT NULL,
  `api_code` varchar(64) NOT NULL DEFAULT '',
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
  PRIMARY KEY (`bucket_start`, `interface_id`, `user_id`),
  KEY `idx_bp_call_api_metric_hour_user_time` (`user_id`, `bucket_start`),
  KEY `idx_bp_call_api_metric_hour_interface_time` (`interface_id`, `bucket_start`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='Per API and user call metrics by hour; replaces the minute grain for wide windows';

-- Backfill: collapse the minute projections into hour buckets. The window is
-- bounded to the hour layer's retention so the migration stays cheap on a
-- large minute table while still covering every bucket a reader can ask for.
INSERT INTO `bp_call_metric_hour` (
  `bucket_start`,`call_count`,`success_count`,`failure_count`,`billable_count`,
  `charge_amount`,`point_amount`,`request_bytes`,`response_bytes`,`elapsed_sum`,
  `elapsed_min`,`elapsed_max`,`first_seen`,`last_seen`
)
SELECT
  FROM_UNIXTIME(FLOOR(UNIX_TIMESTAMP(`bucket_start`)/3600)*3600),
  SUM(`call_count`),SUM(`success_count`),SUM(`failure_count`),SUM(`billable_count`),
  SUM(`charge_amount`),SUM(`point_amount`),SUM(`request_bytes`),SUM(`response_bytes`),
  SUM(`elapsed_sum`),MIN(`elapsed_min`),MAX(`elapsed_max`),MIN(`first_seen`),MAX(`last_seen`)
FROM `bp_call_metric_minute` FORCE INDEX (`PRIMARY`)
WHERE `bucket_start` >= DATE_SUB(NOW(), INTERVAL 180 DAY)
GROUP BY FROM_UNIXTIME(FLOOR(UNIX_TIMESTAMP(`bucket_start`)/3600)*3600)
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

INSERT INTO `bp_call_user_metric_hour` (
  `bucket_start`,`user_id`,`call_count`,`success_count`,`failure_count`,`billable_count`,
  `charge_amount`,`point_amount`,`request_bytes`,`response_bytes`,`elapsed_sum`,
  `elapsed_min`,`elapsed_max`,`first_seen`,`last_seen`
)
SELECT
  FROM_UNIXTIME(FLOOR(UNIX_TIMESTAMP(`bucket_start`)/3600)*3600),`user_id`,
  SUM(`call_count`),SUM(`success_count`),SUM(`failure_count`),SUM(`billable_count`),
  SUM(`charge_amount`),SUM(`point_amount`),SUM(`request_bytes`),SUM(`response_bytes`),
  SUM(`elapsed_sum`),MIN(`elapsed_min`),MAX(`elapsed_max`),MIN(`first_seen`),MAX(`last_seen`)
FROM `bp_call_user_metric_minute` FORCE INDEX (`PRIMARY`)
WHERE `bucket_start` >= DATE_SUB(NOW(), INTERVAL 180 DAY)
GROUP BY FROM_UNIXTIME(FLOOR(UNIX_TIMESTAMP(`bucket_start`)/3600)*3600),`user_id`
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

INSERT INTO `bp_call_api_metric_hour` (
  `bucket_start`,`interface_id`,`user_id`,`api_code`,`call_count`,`success_count`,
  `failure_count`,`billable_count`,`charge_amount`,`point_amount`,`request_bytes`,
  `response_bytes`,`elapsed_sum`,`elapsed_min`,`elapsed_max`,`first_seen`,`last_seen`
)
SELECT
  FROM_UNIXTIME(FLOOR(UNIX_TIMESTAMP(`bucket_start`)/3600)*3600),`interface_id`,`user_id`,
  MAX(`api_code`),SUM(`call_count`),SUM(`success_count`),SUM(`failure_count`),
  SUM(`billable_count`),SUM(`charge_amount`),SUM(`point_amount`),SUM(`request_bytes`),
  SUM(`response_bytes`),SUM(`elapsed_sum`),MIN(`elapsed_min`),MAX(`elapsed_max`),
  MIN(`first_seen`),MAX(`last_seen`)
FROM `bp_call_api_metric_minute` FORCE INDEX (`PRIMARY`)
WHERE `bucket_start` >= DATE_SUB(NOW(), INTERVAL 180 DAY)
GROUP BY FROM_UNIXTIME(FLOOR(UNIX_TIMESTAMP(`bucket_start`)/3600)*3600),`interface_id`,`user_id`
ON DUPLICATE KEY UPDATE
  `api_code`=VALUES(`api_code`),
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
