-- Compact regional traffic rollup for dashboards and map visualizations.
-- unique_ip_count is additive: each analytics source batch contributes the
-- exact distinct count of non-empty IPs inside that batch/hour/location/user
-- group. Summing it across batches or hours is an estimate and may count a
-- recurring IP more than once. The exact high-cardinality IP-hash rollup from
-- migration 000078 remains available for compatibility and exact audit needs.

CREATE TABLE IF NOT EXISTS `bp_call_region_hour` (
  `bucket_start` datetime NOT NULL,
  `user_id` bigint NOT NULL,
  `country` varchar(64) NOT NULL DEFAULT '',
  `province` varchar(64) NOT NULL DEFAULT '',
  `city` varchar(64) NOT NULL DEFAULT '',
  `isp` varchar(128) NOT NULL DEFAULT '',
  `latitude` decimal(10,6) NOT NULL,
  `longitude` decimal(10,6) NOT NULL,
  `call_count` bigint unsigned NOT NULL DEFAULT 0,
  `success_count` bigint unsigned NOT NULL DEFAULT 0,
  `failure_count` bigint unsigned NOT NULL DEFAULT 0,
  `unique_ip_count` bigint unsigned NOT NULL DEFAULT 0
    COMMENT 'Additive per-source-batch distinct non-empty IP estimate; not cross-window exact',
  `request_bytes` decimal(38,0) NOT NULL DEFAULT 0,
  `response_bytes` decimal(38,0) NOT NULL DEFAULT 0,
  `elapsed_sum` decimal(38,0) NOT NULL DEFAULT 0,
  `first_seen` datetime NOT NULL,
  `last_seen` datetime NOT NULL,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`bucket_start`, `user_id`, `country`, `province`, `city`, `isp`, `latitude`, `longitude`),
  KEY `idx_bp_call_region_hour_time` (`bucket_start`, `country`, `province`, `city`),
  KEY `idx_bp_call_region_hour_user_time` (`user_id`, `bucket_start`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='Compact hourly regional traffic; unique_ip_count is an additive estimate';

-- The 30-day administrator atlas is the widest reader. Seed one extra day so
-- an upgraded 78 database is immediately useful even though its analytics
-- checkpoint has already passed historical raw logs. This bounded backfill
-- reads the retained exact 000078 table instead of the much larger raw log.
INSERT INTO `bp_call_region_hour` (
  `bucket_start`,`user_id`,`country`,`province`,`city`,`isp`,`latitude`,`longitude`,
  `call_count`,`success_count`,`failure_count`,`unique_ip_count`,
  `request_bytes`,`response_bytes`,`elapsed_sum`,`first_seen`,`last_seen`
)
SELECT
  `bucket_start`,`user_id`,`country`,`province`,`city`,`isp`,`latitude`,`longitude`,
  SUM(`call_count`),SUM(`success_count`),SUM(`failure_count`),
  COUNT(DISTINCT CASE
    WHEN `client_ip_hash` <> UNHEX(SHA2('bugpk-region-ip-v1:',256)) THEN `client_ip_hash`
  END),SUM(`request_bytes`),SUM(`response_bytes`),
  SUM(`elapsed_sum`),MIN(`first_seen`),MAX(`last_seen`)
FROM `bp_call_region_ip_hour` FORCE INDEX (`idx_bp_call_region_ip_time`)
WHERE `bucket_start` >= DATE_FORMAT(DATE_SUB(NOW(), INTERVAL 31 DAY),'%Y-%m-%d %H:00:00')
GROUP BY `bucket_start`,`user_id`,`country`,`province`,`city`,`isp`,`latitude`,`longitude`
ON DUPLICATE KEY UPDATE `bucket_start`=VALUES(`bucket_start`);
