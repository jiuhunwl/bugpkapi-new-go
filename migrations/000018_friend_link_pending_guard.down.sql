ALTER TABLE bp_friend_link_application
  DROP INDEX uk_bp_friend_link_pending_url,
  DROP INDEX uk_bp_friend_link_pending_user,
  DROP COLUMN pending_url_guard,
  DROP COLUMN pending_user_guard;
