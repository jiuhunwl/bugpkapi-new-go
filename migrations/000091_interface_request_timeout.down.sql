SET @bugpk_schema = DATABASE();

SET @bugpk_sql = IF(EXISTS(
  SELECT 1 FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_schema
    AND TABLE_NAME = 'bp_interface_global_delivery_policy'
    AND COLUMN_NAME = 'request_timeout_ms'
), 'ALTER TABLE `bp_interface_global_delivery_policy` DROP COLUMN `request_timeout_ms`', 'SELECT 1 WHERE 0');
PREPARE bugpk_stmt FROM @bugpk_sql;
EXECUTE bugpk_stmt;
DEALLOCATE PREPARE bugpk_stmt;

SET @bugpk_sql = IF(EXISTS(
  SELECT 1 FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_schema
    AND TABLE_NAME = 'bp_interface_api'
    AND COLUMN_NAME = 'request_timeout_override'
), 'ALTER TABLE `bp_interface_api` DROP COLUMN `request_timeout_override`', 'SELECT 1 WHERE 0');
PREPARE bugpk_stmt FROM @bugpk_sql;
EXECUTE bugpk_stmt;
DEALLOCATE PREPARE bugpk_stmt;

SET @bugpk_sql = IF(EXISTS(
  SELECT 1 FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_schema
    AND TABLE_NAME = 'bp_interface_api'
    AND COLUMN_NAME = 'request_timeout_ms'
), 'ALTER TABLE `bp_interface_api` DROP COLUMN `request_timeout_ms`', 'SELECT 1 WHERE 0');
PREPARE bugpk_stmt FROM @bugpk_sql;
EXECUTE bugpk_stmt;
DEALLOCATE PREPARE bugpk_stmt;
