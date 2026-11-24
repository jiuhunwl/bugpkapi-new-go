-- Remove only untouched seed rows. Configured providers (credentials, enabled
-- state, or a later update timestamp) are preserved during rollback.
DELETE FROM `bp_captcha_provider_config`
WHERE `scene` = 'default'
  AND `provider_key` IN ('geetest4', 'cloudflare')
  AND `enabled` = 0
  AND TRIM(COALESCE(`site_key`, '')) = ''
  AND TRIM(COALESCE(`secret_ref`, '')) = ''
  AND `timeout_ms` = 3000
  AND LOWER(TRIM(`failure_policy`)) = 'deny'
  AND `updated_at` = `created_at`;
