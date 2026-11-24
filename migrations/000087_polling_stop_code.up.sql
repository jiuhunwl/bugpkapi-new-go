-- ============================================================
-- 000087: interface polling stop code (configurable stop-polling code)
-- Adds bp_interface_api.polling_stop_code: when a node's response
-- body `code` field (default field, see polling_check_field) equals
-- this configured value, the gateway stops polling and returns that
-- node's response directly (Success=false, not counted as a health
-- failure, no cooldown).
-- Empty (NULL / '') keeps the default behavior: only an upstream
-- HTTP 404 terminates polling immediately; all other non-2xx / network
-- errors keep trying every remaining candidate.
-- ============================================================

ALTER TABLE `bp_interface_api`
  ADD COLUMN `polling_stop_code` varchar(255) DEFAULT NULL
    COMMENT '终止轮询业务码：响应体polling_check_field(默认code)等于该值时终止轮询并透传；空=保留HTTP404终止兜底' AFTER `polling_check_expected`;

-- Reassert the aggregate failure-log capacity for installations whose schema
-- version reached 73+ but whose column definition drifted back to VARCHAR.
ALTER TABLE `bp_interface_call_log`
  MODIFY COLUMN `upstream_url` TEXT DEFAULT NULL COMMENT '上游节点引用（不可逆脱敏，多节点）';
