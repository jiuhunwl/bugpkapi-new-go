-- Keep provider selection (`enabled`) separate from the scene-level switch.
-- This lets an administrator disable a scene without losing its provider or
-- encrypted credentials, and lets it be enabled again without reconfiguration.
ALTER TABLE `bp_captcha_provider_config`
  ADD COLUMN `scene_enabled` tinyint NOT NULL DEFAULT 1
    COMMENT 'Whether this authentication scene requires a captcha'
    AFTER `scene`;

-- Preserve the effective intent of existing installations: a scene with no
-- active provider was already treated as disabled by the old admin setting.
UPDATE `bp_captcha_provider_config` AS config
JOIN (
  SELECT `scene`, MAX(CASE WHEN `enabled` = 1 THEN 1 ELSE 0 END) AS `scene_enabled`
  FROM `bp_captcha_provider_config`
  GROUP BY `scene`
) AS states ON states.`scene` = config.`scene`
SET config.`scene_enabled` = states.`scene_enabled`;
