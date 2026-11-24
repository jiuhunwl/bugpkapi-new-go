-- Global interface delivery policy with independent per-interface overrides.
-- Effective order: interface override > global policy > system defaults.
--
-- Compatibility repair: an earlier package could mark schema version 48 after
-- applying this policy while still missing the recoverable API-key column.
-- Keep the repair here as well so upgrading such a database converges safely.

SET @bugpk_schema = DATABASE();

SET @bugpk_sql = IF(EXISTS(
  SELECT 1 FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_schema
    AND TABLE_NAME = 'bp_api_key'
    AND COLUMN_NAME = 'secret_ciphertext'
), 'SELECT 1 WHERE 0',
  'ALTER TABLE `bp_api_key` ADD COLUMN `secret_ciphertext` varchar(512) DEFAULT NULL COMMENT ''API Key可恢复密文（AES-GCM）'' AFTER `key_hash`');
PREPARE bugpk_stmt FROM @bugpk_sql;
EXECUTE bugpk_stmt;
DEALLOCATE PREPARE bugpk_stmt;

SET @bugpk_add_cache_override = IF(EXISTS(
  SELECT 1 FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_schema
    AND TABLE_NAME = 'bp_interface_api'
    AND COLUMN_NAME = 'cache_policy_override'
), 0, 1);
SET @bugpk_sql = IF(@bugpk_add_cache_override = 1,
  'ALTER TABLE `bp_interface_api` ADD COLUMN `cache_policy_override` tinyint NOT NULL DEFAULT 0 COMMENT ''1=use interface cache policy, 0=inherit global'' AFTER `polling_check_expected`',
  'SELECT 1 WHERE 0');
PREPARE bugpk_stmt FROM @bugpk_sql;
EXECUTE bugpk_stmt;
DEALLOCATE PREPARE bugpk_stmt;

SET @bugpk_add_ad_override = IF(EXISTS(
  SELECT 1 FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_schema
    AND TABLE_NAME = 'bp_interface_api'
    AND COLUMN_NAME = 'response_ad_policy_override'
), 0, 1);
SET @bugpk_sql = IF(@bugpk_add_ad_override = 1,
  'ALTER TABLE `bp_interface_api` ADD COLUMN `response_ad_policy_override` tinyint NOT NULL DEFAULT 0 COMMENT ''1=use interface advertisement policy, 0=inherit global'' AFTER `response_ad_field`',
  'SELECT 1 WHERE 0');
PREPARE bugpk_stmt FROM @bugpk_sql;
EXECUTE bugpk_stmt;
DEALLOCATE PREPARE bugpk_stmt;

SET @bugpk_sql = IF(EXISTS(
  SELECT 1 FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_schema
    AND TABLE_NAME = 'bp_interface_api'
    AND COLUMN_NAME = 'delivery_policy_version'
), 'SELECT 1 WHERE 0',
  'ALTER TABLE `bp_interface_api` ADD COLUMN `delivery_policy_version` bigint NOT NULL DEFAULT 1 COMMENT ''Version used to invalidate response-cache keys'' AFTER `response_ad_policy_override`');
PREPARE bugpk_stmt FROM @bugpk_sql;
EXECUTE bugpk_stmt;
DEALLOCATE PREPARE bugpk_stmt;

SET @bugpk_sql = IF(EXISTS(
  SELECT 1 FROM information_schema.STATISTICS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_schema
    AND TABLE_NAME = 'bp_interface_api'
    AND INDEX_NAME = 'idx_bp_interface_cache_override'
), 'SELECT 1 WHERE 0',
  'ALTER TABLE `bp_interface_api` ADD INDEX `idx_bp_interface_cache_override` (`cache_policy_override`)');
PREPARE bugpk_stmt FROM @bugpk_sql;
EXECUTE bugpk_stmt;
DEALLOCATE PREPARE bugpk_stmt;

SET @bugpk_sql = IF(EXISTS(
  SELECT 1 FROM information_schema.STATISTICS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_schema
    AND TABLE_NAME = 'bp_interface_api'
    AND INDEX_NAME = 'idx_bp_interface_ad_override'
), 'SELECT 1 WHERE 0',
  'ALTER TABLE `bp_interface_api` ADD INDEX `idx_bp_interface_ad_override` (`response_ad_policy_override`)');
PREPARE bugpk_stmt FROM @bugpk_sql;
EXECUTE bugpk_stmt;
DEALLOCATE PREPARE bugpk_stmt;

-- Existing rows previously had only interface-local settings. Backfill only
-- columns introduced by this run so reruns never reset an administrator's
-- deliberate choice to inherit the global policy.
SET @bugpk_sql = IF(@bugpk_add_cache_override = 1,
  'UPDATE `bp_interface_api` SET `cache_policy_override` = 1 WHERE `id` > 0',
  'SELECT 1 WHERE 0');
PREPARE bugpk_stmt FROM @bugpk_sql;
EXECUTE bugpk_stmt;
DEALLOCATE PREPARE bugpk_stmt;

SET @bugpk_sql = IF(@bugpk_add_ad_override = 1,
  'UPDATE `bp_interface_api` SET `response_ad_policy_override` = 1 WHERE `id` > 0',
  'SELECT 1 WHERE 0');
PREPARE bugpk_stmt FROM @bugpk_sql;
EXECUTE bugpk_stmt;
DEALLOCATE PREPARE bugpk_stmt;

CREATE TABLE IF NOT EXISTS `bp_interface_global_delivery_policy` (
  `id` bigint NOT NULL DEFAULT 1 COMMENT 'Singleton id; always 1',
  `cache_enabled` tinyint NOT NULL DEFAULT 0 COMMENT 'Whether the response cache is enabled',
  `cache_default_ttl_seconds` int NOT NULL DEFAULT 0 COMMENT 'Default cache TTL when no rule matches',
  `cache_rules` json NULL COMMENT 'Conditional response-cache rules',
  `response_ad_enabled` tinyint NOT NULL DEFAULT 0 COMMENT 'Whether to inject an advertisement field',
  `response_ad_id` bigint NULL DEFAULT NULL COMMENT 'Selected API response advertisement; null means automatic',
  `response_ad_field` varchar(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT '_advertisement' COMMENT 'Injected JSON field name',
  `policy_version` bigint NOT NULL DEFAULT 1 COMMENT 'Version used to invalidate response-cache keys',
  `updated_by` bigint NULL DEFAULT NULL,
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`) USING BTREE,
  INDEX `idx_bp_global_delivery_cache` (`cache_enabled`),
  INDEX `idx_bp_global_delivery_ad` (`response_ad_enabled`, `response_ad_id`),
  CONSTRAINT `fk_bp_global_delivery_response_ad` FOREIGN KEY (`response_ad_id`) REFERENCES `bp_advertisement` (`id`) ON DELETE SET NULL ON UPDATE RESTRICT
) ENGINE=InnoDB CHARACTER SET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='Global interface delivery and cache policy' ROW_FORMAT=Dynamic;

SET @bugpk_sql = IF(EXISTS(
  SELECT 1 FROM information_schema.REFERENTIAL_CONSTRAINTS
  WHERE BINARY CONSTRAINT_SCHEMA = BINARY @bugpk_schema
    AND TABLE_NAME = 'bp_interface_global_delivery_policy'
    AND CONSTRAINT_NAME = 'fk_bp_global_delivery_response_ad'
), 'SELECT 1 WHERE 0',
  'ALTER TABLE `bp_interface_global_delivery_policy` ADD CONSTRAINT `fk_bp_global_delivery_response_ad` FOREIGN KEY (`response_ad_id`) REFERENCES `bp_advertisement` (`id`) ON DELETE SET NULL ON UPDATE RESTRICT');
PREPARE bugpk_stmt FROM @bugpk_sql;
EXECUTE bugpk_stmt;
DEALLOCATE PREPARE bugpk_stmt;

INSERT INTO `bp_interface_global_delivery_policy`
  (`id`, `cache_enabled`, `cache_default_ttl_seconds`, `cache_rules`, `response_ad_enabled`, `response_ad_id`, `response_ad_field`, `policy_version`)
VALUES (1, 0, 0, NULL, 0, NULL, '_advertisement', 1)
ON DUPLICATE KEY UPDATE `id` = VALUES(`id`);
