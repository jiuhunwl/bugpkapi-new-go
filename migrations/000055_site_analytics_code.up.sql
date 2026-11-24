ALTER TABLE `bp_site_config`
  ADD COLUMN `analytics_code` mediumtext CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci NULL
    COMMENT 'Public analytics and tracking code snippet'
    AFTER `copyright`;
