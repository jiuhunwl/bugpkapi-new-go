ALTER TABLE `bp_site_config`
  ADD COLUMN `loading_animation_preset` varchar(32) NOT NULL DEFAULT 'gateway'
    COMMENT 'Built-in startup animation preset and custom image fallback'
    AFTER `loading_animation_enabled`;

