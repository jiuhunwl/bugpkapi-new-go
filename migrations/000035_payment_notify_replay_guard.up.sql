ALTER TABLE `bp_payment_notify_log`
  ADD COLUMN `attempt_count` int unsigned NOT NULL DEFAULT 1 COMMENT 'number of deliveries observed for this notification' AFTER `payload`,
  ADD COLUMN `last_seen_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT 'last delivery time' AFTER `created_at`;

UPDATE `bp_payment_notify_log` target
INNER JOIN (
  SELECT event_type, notify_id, COUNT(*) attempts, MAX(created_at) last_seen
  FROM `bp_payment_notify_log`
  WHERE notify_id IS NOT NULL AND notify_id <> ''
  GROUP BY event_type, notify_id
) aggregate_rows
  ON aggregate_rows.event_type = target.event_type
 AND aggregate_rows.notify_id = target.notify_id
SET target.attempt_count = aggregate_rows.attempts,
    target.last_seen_at = aggregate_rows.last_seen;

DELETE loser
FROM `bp_payment_notify_log` loser
INNER JOIN `bp_payment_notify_log` winner
  ON winner.event_type = loser.event_type
 AND winner.notify_id = loser.notify_id
 AND (
      winner.processed > loser.processed
      OR (winner.processed = loser.processed AND winner.verified > loser.verified)
      OR (winner.processed = loser.processed AND winner.verified = loser.verified AND winner.id < loser.id)
 )
WHERE loser.notify_id IS NOT NULL
  AND loser.notify_id <> '';

ALTER TABLE `bp_payment_notify_log`
  ADD UNIQUE KEY `uk_bp_payment_notify_event_id` (`event_type`, `notify_id`);
