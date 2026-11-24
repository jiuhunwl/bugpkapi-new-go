ALTER TABLE `bp_interface_call_log`
  ADD KEY `idx_bp_call_log_home_window` (
    `created_at`,
    `charge_type`,
    `user_id`,
    `success`,
    `ip_province`,
    `ip_city`,
    `client_ip`
  ),
  ADD KEY `idx_bp_call_log_access_window` (
    `created_at`,
    `charge_type`,
    `user_id`,
    `ip_country`,
    `ip_province`,
    `ip_city`,
    `ip_isp`,
    `latitude`,
    `longitude`
  ),
  ADD KEY `idx_bp_call_log_failure_ip` (
    `created_at`,
    `charge_type`,
    `success`,
    `client_ip`,
    `interface_id`
  ),
  ADD KEY `idx_bp_call_log_failure_user` (
    `created_at`,
    `charge_type`,
    `success`,
    `user_id`,
    `interface_id`
  );
