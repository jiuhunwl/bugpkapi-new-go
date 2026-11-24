UPDATE bp_friend_link_application current_app
JOIN bp_friend_link_application earlier_app
  ON earlier_app.id < current_app.id
 AND earlier_app.status = 'PENDING'
 AND current_app.status = 'PENDING'
 AND (earlier_app.user_id = current_app.user_id OR earlier_app.normalized_site_url = current_app.normalized_site_url)
SET current_app.status = 'REJECTED',
    current_app.reject_reason = 'Duplicate pending application migrated by system',
    current_app.review_time = NOW();

ALTER TABLE bp_friend_link_application
  ADD COLUMN pending_user_guard binary(32)
    GENERATED ALWAYS AS (CASE WHEN status = 'PENDING' THEN UNHEX(SHA2(CAST(user_id AS CHAR), 256)) ELSE NULL END) STORED,
  ADD COLUMN pending_url_guard binary(32)
    GENERATED ALWAYS AS (CASE WHEN status = 'PENDING' THEN UNHEX(SHA2(normalized_site_url, 256)) ELSE NULL END) STORED,
  ADD UNIQUE KEY uk_bp_friend_link_pending_user (pending_user_guard),
  ADD UNIQUE KEY uk_bp_friend_link_pending_url (pending_url_guard);
