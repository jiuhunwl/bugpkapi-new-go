-- Roll back application readers/writers first. Audit rows are NOT deleted.
-- Removing this snapshot loses role-at-event evidence; reapply leaves it unknown.
ALTER TABLE bp_operation_log
 DROP INDEX idx_bp_operation_log_user_actor_time,
 DROP INDEX idx_bp_operation_log_actor_time,
 DROP COLUMN actor_kind;
