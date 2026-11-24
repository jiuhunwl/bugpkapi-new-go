ALTER TABLE `bp_user`
  DROP KEY `idx_bp_user_ban_state`,
  DROP COLUMN `ban_reason`,
  DROP COLUMN `ban_until`,
  DROP COLUMN `ban_permanent`;
