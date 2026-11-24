-- Persist the expensive fixed 24-hour risk-control overview. The HTTP path
-- reads one constant-cardinality row while a serial background worker refreshes
-- the payload from the exact rollups.
CREATE TABLE IF NOT EXISTS `bp_risk_control_overview_snapshot` (
  `window_minutes` smallint unsigned NOT NULL,
  `payload` json NOT NULL,
  `source_updated_at` datetime(6) NOT NULL,
  `created_at` datetime(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  `updated_at` datetime(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
  PRIMARY KEY (`window_minutes`),
  CONSTRAINT `chk_bp_risk_overview_window` CHECK (`window_minutes` > 0 AND `window_minutes` <= 1440)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='Persisted fixed-window risk-control dashboard snapshots';

-- Candidate detection reads only these key columns plus has_client_ip and
-- call_count. Extending the existing rule-specific indexes makes the range
-- scan covering and avoids one clustered-row lookup per contributor.
ALTER TABLE `bp_call_risk_fingerprint_5m`
  DROP INDEX `idx_bp_call_risk_single`,
  DROP INDEX `idx_bp_call_risk_proxy`,
  DROP INDEX `idx_bp_call_risk_distributed`,
  ADD INDEX `idx_bp_call_risk_single` (`bucket_start`, `client_ip_hash`, `interface_id`, `request_fingerprint`, `has_client_ip`, `call_count`),
  ADD INDEX `idx_bp_call_risk_proxy` (`bucket_start`, `user_id`, `interface_id`, `request_fingerprint`, `has_client_ip`, `client_ip_hash`, `call_count`),
  ADD INDEX `idx_bp_call_risk_distributed` (`bucket_start`, `interface_id`, `request_fingerprint`, `has_client_ip`, `client_ip_hash`, `user_id`, `call_count`);
