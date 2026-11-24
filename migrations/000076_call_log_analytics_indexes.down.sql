ALTER TABLE `bp_interface_call_log`
  DROP KEY `idx_bp_call_log_failure_user`,
  DROP KEY `idx_bp_call_log_failure_ip`,
  DROP KEY `idx_bp_call_log_access_window`,
  DROP KEY `idx_bp_call_log_home_window`;
