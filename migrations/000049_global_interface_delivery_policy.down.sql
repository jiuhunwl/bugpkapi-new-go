SET @bugpk_schema = DATABASE();

DROP TABLE IF EXISTS `bp_interface_global_delivery_policy`;

SET @bugpk_sql = IF(EXISTS(
  SELECT 1 FROM information_schema.STATISTICS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_schema
    AND TABLE_NAME = 'bp_interface_api'
    AND INDEX_NAME = 'idx_bp_interface_ad_override'
), 'ALTER TABLE `bp_interface_api` DROP INDEX `idx_bp_interface_ad_override`',
  'SELECT 1 WHERE 0');
PREPARE bugpk_stmt FROM @bugpk_sql;
EXECUTE bugpk_stmt;
DEALLOCATE PREPARE bugpk_stmt;

SET @bugpk_sql = IF(EXISTS(
  SELECT 1 FROM information_schema.STATISTICS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_schema
    AND TABLE_NAME = 'bp_interface_api'
    AND INDEX_NAME = 'idx_bp_interface_cache_override'
), 'ALTER TABLE `bp_interface_api` DROP INDEX `idx_bp_interface_cache_override`',
  'SELECT 1 WHERE 0');
PREPARE bugpk_stmt FROM @bugpk_sql;
EXECUTE bugpk_stmt;
DEALLOCATE PREPARE bugpk_stmt;

SET @bugpk_sql = IF(EXISTS(
  SELECT 1 FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_schema
    AND TABLE_NAME = 'bp_interface_api'
    AND COLUMN_NAME = 'delivery_policy_version'
), 'ALTER TABLE `bp_interface_api` DROP COLUMN `delivery_policy_version`',
  'SELECT 1 WHERE 0');
PREPARE bugpk_stmt FROM @bugpk_sql;
EXECUTE bugpk_stmt;
DEALLOCATE PREPARE bugpk_stmt;

SET @bugpk_sql = IF(EXISTS(
  SELECT 1 FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_schema
    AND TABLE_NAME = 'bp_interface_api'
    AND COLUMN_NAME = 'response_ad_policy_override'
), 'ALTER TABLE `bp_interface_api` DROP COLUMN `response_ad_policy_override`',
  'SELECT 1 WHERE 0');
PREPARE bugpk_stmt FROM @bugpk_sql;
EXECUTE bugpk_stmt;
DEALLOCATE PREPARE bugpk_stmt;

SET @bugpk_sql = IF(EXISTS(
  SELECT 1 FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_schema
    AND TABLE_NAME = 'bp_interface_api'
    AND COLUMN_NAME = 'cache_policy_override'
), 'ALTER TABLE `bp_interface_api` DROP COLUMN `cache_policy_override`',
  'SELECT 1 WHERE 0');
PREPARE bugpk_stmt FROM @bugpk_sql;
EXECUTE bugpk_stmt;
DEALLOCATE PREPARE bugpk_stmt;
