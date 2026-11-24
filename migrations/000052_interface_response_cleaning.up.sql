-- Optional per-interface normalization of short-video upstream JSON responses.
-- Disabled by default so existing interfaces continue to pass through their
-- upstream response unchanged.
ALTER TABLE `bp_interface_api`
  ADD COLUMN `response_cleaning_enabled` tinyint NOT NULL DEFAULT 0
    COMMENT 'Whether short-video upstream JSON response cleaning is enabled'
    AFTER `polling_check_expected`;
