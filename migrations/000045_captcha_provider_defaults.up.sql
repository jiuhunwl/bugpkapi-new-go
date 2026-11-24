-- Ensure every concrete third-party captcha implementation is represented by
-- a safe, disabled default row. Existing credentials and administrator choices
-- are never overwritten.
INSERT INTO `bp_captcha_provider_config`
  (`provider_key`, `scene`, `enabled`, `timeout_ms`, `failure_policy`, `extra_config`)
VALUES
  ('geetest4', 'default', 0, 3000, 'deny', JSON_OBJECT('product', 'float')),
  ('cloudflare', 'default', 0, 3000, 'deny', JSON_OBJECT())
ON DUPLICATE KEY UPDATE
  `provider_key` = VALUES(`provider_key`);

-- The runtime only supports fail-closed verification. Normalize legacy rows so
-- database state cannot silently request an unsupported fail-open policy.
UPDATE `bp_captcha_provider_config`
SET `failure_policy` = 'deny'
WHERE `failure_policy` IS NULL OR LOWER(TRIM(`failure_policy`)) <> 'deny';
