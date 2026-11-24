-- Event-time audience snapshot. Existing rows remain untouched/unclassified:
-- today's roles cannot reliably reconstruct historic roles or legacy noise.
-- Apply before deploying the scoped audit reader/writer. No historical backfill.
ALTER TABLE bp_operation_log
 ADD COLUMN actor_kind varchar(16) NOT NULL DEFAULT '' COMMENT 'Event-time admin/user; empty means legacy/unclassified',
 ADD KEY idx_bp_operation_log_actor_time (actor_kind, created_at),
 ADD KEY idx_bp_operation_log_user_actor_time (user_id, actor_kind, created_at);
