ALTER TABLE `bp_payment_notify_log`
  DROP KEY `uk_bp_payment_notify_event_id`,
  DROP COLUMN `last_seen_at`,
  DROP COLUMN `attempt_count`;
