-- Slow-query optimization: cover the two remaining high-frequency access
-- patterns that were still scanning historical rows without an index that
-- matches their WHERE + ORDER BY shape.

-- 1) Call-log list/count filtered by (user_id, user_visible) and ordered by
--    created_at. The previous idx_bp_interface_call_log_user (user_id,
--    created_at) still had to read the clustered row to evaluate user_visible
--    and could not serve the "my logs" paging path as an index-only scan.
--    (id) is the InnoDB implicit primary-key suffix of every secondary index,
--    so (user_id, user_visible, created_at) also satisfies ORDER BY
--    created_at, id.
ALTER TABLE `bp_interface_call_log`
  ADD KEY `idx_bp_call_log_user_visible` (`user_id`, `user_visible`, `created_at`);

-- 2) Confirmed-revenue materialization sums charge_amount for every row with
--    status='CONFIRMED'. A covering (status, charge_amount) index turns that
--    backfill into an index-only scan instead of a clustered fetch per row.
--    The steady-state dashboard already reads bp_billing_revenue_total; this
--    index only protects the one-time/backfill SELECT SUM(...) path.
ALTER TABLE `bp_billing_record`
  ADD KEY `idx_bp_billing_record_status_amount` (`status`, `charge_amount`);
