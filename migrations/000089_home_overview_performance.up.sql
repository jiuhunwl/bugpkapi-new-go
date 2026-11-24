-- Reduce the cardinality of the public homepage hot-API reader.
-- Minute rollups remain the source for short-window charts and audit views;
-- this table is the 30-day interface ranking projection.
CREATE TABLE IF NOT EXISTS `bp_call_api_metric_day` (
  `bucket_date` date NOT NULL,
  `interface_id` bigint NOT NULL,
  `user_id` bigint NOT NULL,
  `api_code` varchar(64) NOT NULL DEFAULT '',
  `call_count` bigint unsigned NOT NULL DEFAULT 0,
  `success_count` bigint unsigned NOT NULL DEFAULT 0,
  `failure_count` bigint unsigned NOT NULL DEFAULT 0,
  `elapsed_sum` decimal(38,0) NOT NULL DEFAULT 0,
  `first_seen` datetime NOT NULL,
  `last_seen` datetime NOT NULL,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`bucket_date`, `interface_id`, `user_id`),
  KEY `idx_bp_call_api_day_interface_date` (`interface_id`, `bucket_date`),
  KEY `idx_bp_call_api_day_user_date` (`user_id`, `bucket_date`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='Per API and user daily call metrics for bounded 30-day rankings';

-- Backfill from the compact minute projection instead of the append-only log.
-- The range is intentionally one day wider than the public 30-day window so
-- an upgrade at a day boundary does not produce an empty first ranking.
INSERT INTO `bp_call_api_metric_day` (
  `bucket_date`,`interface_id`,`user_id`,`api_code`,`call_count`,
  `success_count`,`failure_count`,`elapsed_sum`,`first_seen`,`last_seen`
)
SELECT
  DATE(`bucket_start`),`interface_id`,`user_id`,MAX(`api_code`),SUM(`call_count`),
  SUM(`success_count`),SUM(`failure_count`),SUM(`elapsed_sum`),
  MIN(`first_seen`),MAX(`last_seen`)
FROM `bp_call_api_metric_minute` FORCE INDEX (`PRIMARY`)
WHERE `bucket_start` >= DATE_SUB(CURRENT_DATE(), INTERVAL 31 DAY)
GROUP BY DATE(`bucket_start`),`interface_id`,`user_id`
ON DUPLICATE KEY UPDATE
  `api_code`=VALUES(`api_code`),
  `call_count`=VALUES(`call_count`),
  `success_count`=VALUES(`success_count`),
  `failure_count`=VALUES(`failure_count`),
  `elapsed_sum`=VALUES(`elapsed_sum`),
  `first_seen`=VALUES(`first_seen`),
  `last_seen`=VALUES(`last_seen`);

-- The HTTP path reads this constant-cardinality row. The NULL source time
-- marks the seeded row as not ready; the first background refresh replaces it
-- with a measured snapshot. This avoids returning a misleading zero snapshot
-- during startup while still preventing an unbounded request-time scan.
CREATE TABLE IF NOT EXISTS `bp_home_overview_snapshot` (
  `id` tinyint unsigned NOT NULL,
  `payload` json NOT NULL,
  `source_updated_at` datetime(6) DEFAULT NULL,
  `created_at` datetime(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  `updated_at` datetime(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
  PRIMARY KEY (`id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='Persisted public homepage analytics snapshot';

INSERT INTO `bp_home_overview_snapshot` (`id`,`payload`,`source_updated_at`)
VALUES (
  1,
  JSON_OBJECT(
    'stats',JSON_OBJECT(
      'activeRegions24h',0,
      'activeRegions24hDelta',0,
      'apiTotal',0,
      'availabilityPercent','0',
      'availabilitySource','CALL_SUCCESS',
      'calls24h',0,
      'calls24hDeltaPercent','0',
      'enabledApiTotal',0,
      'peakQps24h',0,
      'peakQps24hDelta',0,
      'userTotal',0
    ),
    'hotApis',JSON_ARRAY()
  ),
  NULL
)
ON DUPLICATE KEY UPDATE `id`=VALUES(`id`);
