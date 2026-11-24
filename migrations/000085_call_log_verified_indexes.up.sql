-- Verify-index migration: add the two composite indexes whose query shapes
-- were validated with EXPLAIN ANALYZE on million-row synthetic data but are
-- still not covered by any existing key:
--
--   1) (client_ip, created_at)      — "filter logs by one client IP, ordered
--      by created_at". Previously every such query fell back to a full
--      clustered scan (the region indexes lead with ip_* dimensions, not
--      client_ip). The InnoDB implicit id suffix keeps ORDER BY created_at,
--      id seekable for keyset pagination.
--
--   2) (interface_id, api_key_id, created_at) — "filter logs by one
--      interface AND one API key", a recurring admin diagnostic shape that
--      old indexes could only serve by post-filtering one side from row
--      lookups. Leading with both equality columns narrows first, and the
--      implicit id suffix again covers ORDER BY created_at, id.
--
-- Deliberately NOT added (verified already covered or low-selectivity):
--   - trace_id: single-column idx_bp_interface_call_log_trace exists and
--     point lookups are already seekable; a (trace_id, created_at) suffix
--     only helps ORDER BY on multi-row trace results, measured negligible.
--   - api_key_id: idx_bp_interface_call_log_key (api_key_id, created_at)
--     already exists since 000004.
--   - api_code: idx_bp_interface_call_log_api_code (api_code, created_at)
--     already exists since 000004.
--   - user_id + user_visible + created_at: idx_bp_call_log_user_visible
--     already exists since 000083.
ALTER TABLE `bp_interface_call_log`
  ADD KEY `idx_bp_call_log_client_ip_created` (`client_ip`, `created_at`),
  ADD KEY `idx_bp_call_log_interface_key_created` (`interface_id`, `api_key_id`, `created_at`);