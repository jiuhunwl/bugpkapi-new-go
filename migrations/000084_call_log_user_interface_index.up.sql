-- User + interface + time composite for the user-scoped call-log list path
-- ("my logs" filtered by one interface, ordered by created_at). The existing
-- idx_bp_call_log_user_visible (user_id, user_visible, created_at) must
-- post-filter interface_id from row lookups for users whose traffic spans
-- many interfaces; this index narrows first. The InnoDB implicit primary-key
-- suffix keeps ORDER BY created_at, id seekable for cursor pagination.
ALTER TABLE `bp_interface_call_log`
  ADD KEY `idx_bp_call_log_user_interface` (`user_id`, `interface_id`, `created_at`);
