-- Incremental call-log rollups. The user-grained tables intentionally retain
-- contributions from every existing user and exclude only ADMIN traffic.
-- Readers join bp_user and apply the effective-account-status predicate at
-- query time, so a later ban/unban is reflected without rebuilding history.

CREATE TABLE IF NOT EXISTS `bp_call_metric_minute` (
  `bucket_start` datetime NOT NULL COMMENT 'UTC/local database minute bucket',
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
  COMMENT='Operational all-user call metrics by minute; active-account totals come from bp_call_user_metric_minute';

CREATE TABLE IF NOT EXISTS `bp_call_api_metric_minute` (
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
  KEY `idx_bp_call_api_metric_user_time` (`user_id`, `bucket_start`),
  KEY `idx_bp_call_api_metric_interface_time` (`interface_id`, `bucket_start`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='Per API and user call metrics by minute';

CREATE TABLE IF NOT EXISTS `bp_call_user_metric_minute` (
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
  KEY `idx_bp_call_user_metric_user_time` (`user_id`, `bucket_start`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='Authoritative user contribution metrics by minute for current-account-aware totals';

CREATE TABLE IF NOT EXISTS `bp_call_region_ip_hour` (
  `bucket_start` datetime NOT NULL,
  `user_id` bigint NOT NULL,
  `country` varchar(64) NOT NULL DEFAULT '',
  `province` varchar(64) NOT NULL DEFAULT '',
  `city` varchar(64) NOT NULL DEFAULT '',
  `isp` varchar(128) NOT NULL DEFAULT '',
  `latitude` decimal(10,6) NOT NULL,
  `longitude` decimal(10,6) NOT NULL,
  `client_ip_hash` binary(32) NOT NULL COMMENT 'SHA-256 binary digest; no plaintext IP is retained',
  `call_count` bigint unsigned NOT NULL DEFAULT 0,
  `success_count` bigint unsigned NOT NULL DEFAULT 0,
  `failure_count` bigint unsigned NOT NULL DEFAULT 0,
  `request_bytes` decimal(38,0) NOT NULL DEFAULT 0,
  `response_bytes` decimal(38,0) NOT NULL DEFAULT 0,
  `elapsed_sum` decimal(38,0) NOT NULL DEFAULT 0,
  `first_seen` datetime NOT NULL,
  `last_seen` datetime NOT NULL,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`bucket_start`, `user_id`, `country`, `province`, `city`, `isp`, `latitude`, `longitude`, `client_ip_hash`),
  KEY `idx_bp_call_region_ip_time` (`bucket_start`, `country`, `province`, `city`),
  KEY `idx_bp_call_region_ip_user_time` (`user_id`, `bucket_start`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='Hourly region/IP-hash contributions supporting exact cross-bucket distinct-IP counts';

CREATE TABLE IF NOT EXISTS `bp_call_failure_ip_hour` (
  `bucket_start` datetime NOT NULL,
  `client_ip` varchar(64) NOT NULL COMMENT 'Administrator failure investigation requires the source IP',
  `user_id` bigint NOT NULL,
  `interface_id` bigint NOT NULL,
  `api_code` varchar(64) NOT NULL DEFAULT '',
  `country` varchar(64) NOT NULL DEFAULT '',
  `province` varchar(64) NOT NULL DEFAULT '',
  `city` varchar(64) NOT NULL DEFAULT '',
  `failure_count` bigint unsigned NOT NULL DEFAULT 0,
  `elapsed_sum` decimal(38,0) NOT NULL DEFAULT 0,
  `first_seen` datetime NOT NULL,
  `last_seen` datetime NOT NULL,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`bucket_start`, `client_ip`, `user_id`, `interface_id`),
  KEY `idx_bp_call_failure_ip_rank` (`bucket_start`, `failure_count`, `client_ip`),
  KEY `idx_bp_call_failure_ip_user_time` (`user_id`, `bucket_start`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='Hourly failed-call contributions by source IP, user and API';

CREATE TABLE IF NOT EXISTS `bp_call_failure_user_hour` (
  `bucket_start` datetime NOT NULL,
  `user_id` bigint NOT NULL,
  `interface_id` bigint NOT NULL,
  `api_code` varchar(64) NOT NULL DEFAULT '',
  `client_ip_hash` binary(32) NOT NULL,
  `has_client_ip` tinyint NOT NULL DEFAULT 0,
  `failure_count` bigint unsigned NOT NULL DEFAULT 0,
  `elapsed_sum` decimal(38,0) NOT NULL DEFAULT 0,
  `first_seen` datetime NOT NULL,
  `last_seen` datetime NOT NULL,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`bucket_start`, `user_id`, `interface_id`, `client_ip_hash`),
  KEY `idx_bp_call_failure_user_rank` (`bucket_start`, `failure_count`, `user_id`),
  KEY `idx_bp_call_failure_user_user_time` (`user_id`, `bucket_start`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='Hourly failed-call contributions by user, API and privacy-safe IP hash';

CREATE TABLE IF NOT EXISTS `bp_call_risk_fingerprint_5m` (
  `bucket_start` datetime NOT NULL,
  `interface_id` bigint NOT NULL,
  `request_fingerprint` char(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `user_id` bigint NOT NULL,
  `api_key_id` bigint NOT NULL DEFAULT 0,
  `client_ip_hash` binary(32) NOT NULL,
  `has_client_ip` tinyint NOT NULL DEFAULT 0,
  `client_ip` varchar(64) NOT NULL DEFAULT '',
  `api_code` varchar(64) NOT NULL DEFAULT '',
  `country` varchar(64) NOT NULL DEFAULT '',
  `province` varchar(64) NOT NULL DEFAULT '',
  `city` varchar(64) NOT NULL DEFAULT '',
  `call_count` bigint unsigned NOT NULL DEFAULT 0,
  `success_count` bigint unsigned NOT NULL DEFAULT 0,
  `failure_count` bigint unsigned NOT NULL DEFAULT 0,
  `elapsed_sum` decimal(38,0) NOT NULL DEFAULT 0,
  `first_seen` datetime NOT NULL,
  `last_seen` datetime NOT NULL,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`bucket_start`, `interface_id`, `request_fingerprint`, `user_id`, `api_key_id`, `client_ip_hash`),
  KEY `idx_bp_call_risk_single` (`bucket_start`, `client_ip_hash`, `interface_id`, `request_fingerprint`),
  KEY `idx_bp_call_risk_proxy` (`bucket_start`, `user_id`, `interface_id`, `request_fingerprint`),
  KEY `idx_bp_call_risk_distributed` (`bucket_start`, `interface_id`, `request_fingerprint`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='Five-minute exact contributors for single-IP, proxy-pool and distributed fingerprint rules';

CREATE TABLE IF NOT EXISTS `bp_analytics_checkpoint` (
  `worker_key` varchar(64) NOT NULL,
  `last_log_id` bigint unsigned NOT NULL DEFAULT 0,
  `last_log_created_at` datetime DEFAULT NULL,
  `source_rows` bigint unsigned NOT NULL DEFAULT 0,
  `contribution_rows` bigint unsigned NOT NULL DEFAULT 0,
  `last_batch_size` int unsigned NOT NULL DEFAULT 0,
  `last_started_at` datetime DEFAULT NULL,
  `last_completed_at` datetime DEFAULT NULL,
  `last_error` varchar(1000) NOT NULL DEFAULT '',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`worker_key`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='Idempotent high-water checkpoints for analytics workers';

INSERT IGNORE INTO `bp_analytics_checkpoint` (`worker_key`) VALUES ('call_log_rollup');
