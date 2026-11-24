-- Request an idempotent, bounded repair when a retained minute day has no
-- matching durable daily projection. This fixes pre-000100 historical holes
-- without replaying or deleting history that is no longer in the source log.
--
-- Run with the updated binary: its day repair window is frozen at the source
-- floor and retention protects all PENDING/RUNNING repair rows.
-- Persisted bit order 0..10 is unchanged; mask=2047 repairs all current
-- projections together. A historical day hole may coexist with >1000 missing
-- minute contributions, so a day-only repair cannot certify the recovered window.
-- Do NOT reset an active task or checkpoint. Re-running the standalone upgrade
-- only requests work while an actual missing day still exists.
UPDATE bp_analytics_checkpoint c
SET c.repair_status='PENDING',
    c.repair_phase='IDLE',
    c.repair_projection_mask=2047,
    c.repair_window_days=31,
    c.repair_requested_at=NOW(6),
    c.repair_started_at=NULL,
    c.repair_completed_at=NULL,
    c.repair_window_start=NULL,
    c.repair_upper_log_id=0,
    c.repair_cursor_id=0,
    c.repair_clear_projection=0
WHERE c.worker_key='call_log_rollup'
  AND COALESCE(c.repair_status,'NONE') NOT IN ('PENDING','RUNNING')
  AND COALESCE(c.repair_phase,'IDLE') NOT IN ('CLEAR','ROLLUP','FINALIZE')
  AND EXISTS (
    SELECT 1 FROM (
      SELECT DATE(bucket_start) AS bucket_date,SUM(call_count) AS calls
      FROM bp_call_metric_minute
      WHERE bucket_start>=DATE_SUB(CURRENT_DATE(),INTERVAL 3 DAY)
        AND bucket_start<DATE_ADD(CURRENT_DATE(),INTERVAL 1 DAY)
      GROUP BY DATE(bucket_start)
    ) m
    WHERE m.calls>0 AND (
      NOT EXISTS(SELECT 1 FROM bp_call_metric_day d WHERE d.bucket_date=m.bucket_date AND d.call_count>0)
      OR NOT EXISTS(SELECT 1 FROM bp_call_api_metric_day d WHERE d.bucket_date=m.bucket_date AND d.call_count>0)
      OR NOT EXISTS(SELECT 1 FROM bp_call_user_metric_day d WHERE d.bucket_date=m.bucket_date AND d.call_count>0)
    )
  );
