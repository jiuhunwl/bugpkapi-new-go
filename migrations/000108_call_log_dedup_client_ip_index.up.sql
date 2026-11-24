-- 000108: drop the fully-duplicate client_ip secondary index.
--
-- idx_bp_interface_call_log_client_ip_time (client_ip, created_at) is a
-- byte-for-byte duplicate of idx_bp_call_log_client_ip_created
-- (client_ip, created_at), added later by 000085. Keeping both means every
-- INSERT and every row DELETE maintains two identical B-tree entries, doubling
-- the index-maintenance redo/undo and page writes on the hottest table in the
-- system. Dropping the older one is safe: MySQL chooses either index for the
-- same query shape, and the newer name is the one referenced by EXPLAIN
-- baselines.
--
-- DROP INDEX on a large table requires a brief metadata-lock and an index
-- rebuild; run this in a low-write window. It does not touch data.

ALTER TABLE `bp_interface_call_log`
  DROP INDEX `idx_bp_interface_call_log_client_ip_time`;
