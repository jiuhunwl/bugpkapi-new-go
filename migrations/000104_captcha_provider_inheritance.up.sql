-- Distinguish an administrator's explicit provider choice from the stock seed
-- rows. Every authentication scene ships with its own enabled `pow` row
-- (migration 000001), and the resolver prefers a scene's own row over the
-- `default` row, so switching the `default` scene to 极验 / Cloudflare could
-- never take effect for a real scene. `explicit_provider` makes the intended
-- precedence explicit: a scene's explicit row wins, then the default scene's
-- explicit row (inherited), then the scene's seed row, then the built-in POW
-- fallback.
ALTER TABLE `bp_captcha_provider_config`
  ADD COLUMN `explicit_provider` tinyint NOT NULL DEFAULT 0
    COMMENT 'Whether the administrator explicitly selected this provider: 1 explicit, 0 seed/inherited'
    AFTER `scene_enabled`;

-- Rows that are not the stock `pow` seed, or that already carry third-party
-- credentials, reflect an administrator decision and must stay explicit.
UPDATE `bp_captcha_provider_config`
SET `explicit_provider` = 1
WHERE `provider_key` <> 'pow'
   OR COALESCE(`site_key`, '') <> ''
   OR COALESCE(`secret_ref`, '') <> '';
