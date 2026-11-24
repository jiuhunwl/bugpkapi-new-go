DROP TABLE IF EXISTS `bp_user_risk_state`;
DROP TABLE IF EXISTS `bp_register_event`;

ALTER TABLE `bp_auth_email_code`
  DROP COLUMN `attempts`;

ALTER TABLE `bp_auth_register_config`
  DROP COLUMN `register_risk_reject_score`,
  DROP COLUMN `register_risk_quarantine_score`,
  DROP COLUMN `register_risk_shadow_mode`,
  DROP COLUMN `register_risk_enabled`,
  DROP COLUMN `register_ip_prefix_day_limit`,
  DROP COLUMN `register_device_day_limit`,
  DROP COLUMN `new_account_day_limit`;

ALTER TABLE `bp_role`
  DROP CHECK `chk_bp_role_quota_non_negative`,
  DROP CHECK `chk_bp_role_quota_count_mode`,
  DROP COLUMN `request_quota_count_mode`,
  DROP COLUMN `request_quota_month`,
  DROP COLUMN `request_quota_day`,
  DROP COLUMN `request_quota_hour`,
  DROP COLUMN `request_quota_minute`,
  DROP COLUMN `request_quota_enabled`;
