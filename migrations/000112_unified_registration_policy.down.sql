DROP TABLE IF EXISTS `bp_register_ip_usage`;

ALTER TABLE `bp_auth_oauth_provider`
  DROP COLUMN `reject_silenced`,
  DROP COLUMN `require_active`,
  DROP COLUMN `min_trust_level`,
  DROP COLUMN `min_account_age_days`,
  DROP COLUMN `require_verified_email_mode`,
  DROP COLUMN `register_ip_max_accounts`,
  DROP COLUMN `register_ip_day_limit`,
  DROP COLUMN `register_ip_hour_limit`,
  DROP COLUMN `register_gift_points`,
  DROP COLUMN `default_role_id`;

ALTER TABLE `bp_auth_register_config`
  DROP COLUMN `oauth_require_verified_email`,
  DROP COLUMN `oauth_register_ip_max_accounts`,
  DROP COLUMN `oauth_register_ip_day_limit`,
  DROP COLUMN `oauth_register_ip_hour_limit`,
  DROP COLUMN `oauth_register_mode_default`,
  DROP COLUMN `oauth_register_enabled`,
  DROP COLUMN `register_ip_day_limit`;
