ALTER TABLE `bp_interface_call_log`
  ADD KEY `idx_bp_call_log_risk_window` (
    `created_at`,
    `charge_type`,
    `interface_id`,
    `request_fingerprint`,
    `client_ip`,
    `user_id`,
    `api_key_id`
  );
