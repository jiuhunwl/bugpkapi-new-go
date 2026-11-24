-- Call outcomes are now classified per row instead of being inferred from the
-- single success flag at read time. outcome_class is written by the gateway:
--   0 = unclassified (legacy rows; statistics derive the class from status)
--   1 = SUCCESS
--   2 = EXPECTED_EMPTY (HTTP 404 or a configured business stop code)
--   3 = CLIENT_ERROR
--   4 = THROTTLE
--   5 = UPSTREAM_ERROR
-- Keeping 0 as "derive at read time" avoids a full-table rewrite on a large
-- append-only log; every new row writes an explicit class.
ALTER TABLE `bp_interface_call_log`
  ADD COLUMN `outcome_class` tinyint NOT NULL DEFAULT 0 COMMENT '调用结果分类 0=按状态推导 1=成功 2=预期空结果 3=客户端错误 4=限流 5=上游失败';

-- The three minute-layer projections carry the outcome buckets used by the
-- console composition card and the SLO alerts. Existing rows are rebuilt by
-- the analytics worker through the durable repair task below; until that
-- finishes, readers report the composition as unavailable instead of showing
-- a partial denominator as complete.
ALTER TABLE `bp_call_metric_minute`
  ADD COLUMN `expected_empty_count` bigint unsigned NOT NULL DEFAULT 0 COMMENT '预期空结果数（404/配置业务码）',
  ADD COLUMN `client_error_count` bigint unsigned NOT NULL DEFAULT 0 COMMENT '客户端错误数（4xx，不含404/429）',
  ADD COLUMN `throttle_count` bigint unsigned NOT NULL DEFAULT 0 COMMENT '限流数（429）',
  ADD COLUMN `upstream_error_count` bigint unsigned NOT NULL DEFAULT 0 COMMENT '上游失败数（5xx/网络/超时/2xx业务失败）';

ALTER TABLE `bp_call_user_metric_minute`
  ADD COLUMN `expected_empty_count` bigint unsigned NOT NULL DEFAULT 0 COMMENT '预期空结果数（404/配置业务码）',
  ADD COLUMN `client_error_count` bigint unsigned NOT NULL DEFAULT 0 COMMENT '客户端错误数（4xx，不含404/429）',
  ADD COLUMN `throttle_count` bigint unsigned NOT NULL DEFAULT 0 COMMENT '限流数（429）',
  ADD COLUMN `upstream_error_count` bigint unsigned NOT NULL DEFAULT 0 COMMENT '上游失败数（5xx/网络/超时/2xx业务失败）';

ALTER TABLE `bp_call_api_metric_minute`
  ADD COLUMN `expected_empty_count` bigint unsigned NOT NULL DEFAULT 0 COMMENT '预期空结果数（404/配置业务码）',
  ADD COLUMN `client_error_count` bigint unsigned NOT NULL DEFAULT 0 COMMENT '客户端错误数（4xx，不含404/429）',
  ADD COLUMN `throttle_count` bigint unsigned NOT NULL DEFAULT 0 COMMENT '限流数（429）',
  ADD COLUMN `upstream_error_count` bigint unsigned NOT NULL DEFAULT 0 COMMENT '上游失败数（5xx/网络/超时/2xx业务失败）';

-- Backfill only the three minute projections over the last day. The production
-- call log is tens of GB, so a broad 3-day/all-projection repair would hold the
-- analytics worker for hours. Bits 0/1/3 are metric-minute, api-minute and
-- user-minute; the failure_count predicate is unchanged, so no other projection
-- needs rebuilding. The repair is bounded by the dataset floor and is
-- idempotent: it clears and replays the same minute buckets.
INSERT INTO `bp_analytics_checkpoint`
  (`worker_key`,`repair_status`,`repair_window_days`,`repair_projection_mask`,
   `repair_phase`,`repair_clear_projection`)
VALUES
  ('call_log_rollup','PENDING',1,11,'IDLE',0)
ON DUPLICATE KEY UPDATE
  `repair_window_days`=GREATEST(COALESCE(`repair_window_days`,0),1),
  `repair_projection_mask`=(COALESCE(`repair_projection_mask`,0) | 11),
  `repair_status`='PENDING',
  `repair_phase`='IDLE',
  `repair_requested_at`=NOW(6),
  `repair_started_at`=NULL,
  `repair_completed_at`=NULL,
  `repair_upper_log_id`=0,
  `repair_cursor_id`=0,
  `repair_window_start`=NULL,
  `repair_clear_projection`=0;
