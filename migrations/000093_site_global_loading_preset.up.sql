-- 000093: add the global loading animation preset to the site config.
--
-- The startup splash (loading_animation_preset) and the in-app loading
-- indicator serve different moments, so they are configured independently:
-- the splash runs before the bundle executes, while this preset drives route
-- switches and pending requests. Default 'bar' keeps content visible, which
-- shortens perceived waiting compared with a blocking overlay.

SET @bugpk_global_loading_schema = DATABASE();

SET @bugpk_global_loading_sql = IF(EXISTS(
  SELECT 1 FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_global_loading_schema
    AND TABLE_NAME = 'bp_site_config'
    AND COLUMN_NAME = 'global_loading_preset'
), 'SELECT 1 WHERE 0',
  'ALTER TABLE `bp_site_config` ADD COLUMN `global_loading_preset` varchar(32) NOT NULL DEFAULT ''bar'' COMMENT ''Global loading animation: bar/ring/skeleton/packet/logo/dots'' AFTER `loading_animation_url`');
PREPARE bugpk_global_loading_stmt FROM @bugpk_global_loading_sql;
EXECUTE bugpk_global_loading_stmt;
DEALLOCATE PREPARE bugpk_global_loading_stmt;

-- Normalise any value outside the supported set back to the default so the
-- API never returns a preset the frontend cannot render.
UPDATE `bp_site_config`
SET `global_loading_preset` = 'bar'
WHERE `global_loading_preset` NOT IN (
  'bar', 'ring', 'skeleton', 'packet', 'logo', 'dots'
);

-- The splash gained forge/scan/rail/dots in this release. Legacy rows still
-- holding pulse/bars keep rendering (both remain accepted), but a row created
-- before the preset column existed may hold an empty string.
UPDATE `bp_site_config`
SET `loading_animation_preset` = 'forge'
WHERE `loading_animation_preset` = ''
   OR `loading_animation_preset` IS NULL;
