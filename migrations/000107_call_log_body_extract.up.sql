-- 000107: extract the call-log response body into a side table.
--
-- bp_interface_call_log.response_body is a MEDIUMTEXT that carries a full,
-- masked-and-truncated response JSON per call. Keeping it inline bloats every
-- clustered row and every secondary index rebuild, which is the dominant driver
-- of the table's several-hundred-GB footprint and ~40 GB/day write volume.
--
-- This migration creates the side table WITHOUT backfilling the existing log.
-- A backfill would have to scan the entire multi-hundred-GB source table during
-- deployment, holding locks for hours — exactly what the upgrade contract
-- forbids. Instead:
--   - New writes put the body only in the side table (the main column is left
--     NULL for new rows).
--   - Reads use coalesce(b.response_body, l.response_body): rows written before
--     this migration keep serving their body from the main column.
--   - The main column is dropped later in a separately-scheduled maintenance
--     window, after the read path no longer references it.
--
-- The application writes the body in the same transaction as the main insert,
-- so a main row can never exist without its body (and a body row is always
-- attached to a committed main row via the shared transaction).

CREATE TABLE IF NOT EXISTS `bp_interface_call_log_body` (
  `log_id` bigint NOT NULL COMMENT '对应 bp_interface_call_log.id',
  `response_body` mediumtext COMMENT '响应内容（脱敏+截断后）',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '冗余创建时间，用于与主表同保留期清理',
  PRIMARY KEY (`log_id`),
  KEY `idx_bp_call_log_body_created` (`created_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='调用日志响应体独立存储（大字段外移，详情页点查）';

