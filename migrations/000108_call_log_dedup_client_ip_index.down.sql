-- Restore the duplicate index if a rollback is ever required. Re-adding on a
-- large table rebuilds the index; run in a low-write window.
ALTER TABLE `bp_interface_call_log`
  ADD KEY `idx_bp_interface_call_log_client_ip_time` (`client_ip`, `created_at`);
