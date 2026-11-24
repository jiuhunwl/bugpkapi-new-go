ALTER TABLE `bp_call_risk_fingerprint_5m`
  DROP INDEX `idx_bp_call_risk_single`,
  DROP INDEX `idx_bp_call_risk_proxy`,
  DROP INDEX `idx_bp_call_risk_distributed`,
  ADD INDEX `idx_bp_call_risk_single` (`bucket_start`, `client_ip_hash`, `interface_id`, `request_fingerprint`),
  ADD INDEX `idx_bp_call_risk_proxy` (`bucket_start`, `user_id`, `interface_id`, `request_fingerprint`),
  ADD INDEX `idx_bp_call_risk_distributed` (`bucket_start`, `interface_id`, `request_fingerprint`);

DROP TABLE IF EXISTS `bp_risk_control_overview_snapshot`;
