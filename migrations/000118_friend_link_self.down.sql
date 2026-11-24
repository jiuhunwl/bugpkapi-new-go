ALTER TABLE `bp_friend_link_application`
  DROP KEY `idx_bp_friend_link_app_link`,
  DROP COLUMN `change_kind`,
  DROP COLUMN `link_id`;

ALTER TABLE `bp_friend_link`
  DROP KEY `idx_bp_friend_link_user`,
  DROP COLUMN `user_id`;
