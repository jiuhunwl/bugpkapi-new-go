-- 回滚套餐总调用量 + 计量方式。
SET @bugpk_schema = DATABASE();

SET @bugpk_sql = IF(EXISTS(
  SELECT 1 FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_schema
    AND TABLE_NAME = 'bp_package_global'
    AND COLUMN_NAME = 'total_limit'
), 'ALTER TABLE `bp_package_global` DROP COLUMN `total_limit`, DROP COLUMN `count_mode`', 'SELECT 1 WHERE 0');
PREPARE bugpk_stmt FROM @bugpk_sql;
EXECUTE bugpk_stmt;
DEALLOCATE PREPARE bugpk_stmt;

SET @bugpk_sql = IF(EXISTS(
  SELECT 1 FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_schema
    AND TABLE_NAME = 'bp_package_interface_spec'
    AND COLUMN_NAME = 'total_limit'
), 'ALTER TABLE `bp_package_interface_spec` DROP COLUMN `total_limit`, DROP COLUMN `count_mode`', 'SELECT 1 WHERE 0');
PREPARE bugpk_stmt FROM @bugpk_sql;
EXECUTE bugpk_stmt;
DEALLOCATE PREPARE bugpk_stmt;

SET @bugpk_sql = IF(EXISTS(
  SELECT 1 FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_schema
    AND TABLE_NAME = 'bp_user_package_global'
    AND COLUMN_NAME = 'total_limit'
), 'ALTER TABLE `bp_user_package_global` DROP COLUMN `total_limit`, DROP COLUMN `count_mode`', 'SELECT 1 WHERE 0');
PREPARE bugpk_stmt FROM @bugpk_sql;
EXECUTE bugpk_stmt;
DEALLOCATE PREPARE bugpk_stmt;

SET @bugpk_sql = IF(EXISTS(
  SELECT 1 FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_schema
    AND TABLE_NAME = 'bp_user_package_interface'
    AND COLUMN_NAME = 'total_limit'
), 'ALTER TABLE `bp_user_package_interface` DROP COLUMN `total_limit`, DROP COLUMN `count_mode`', 'SELECT 1 WHERE 0');
PREPARE bugpk_stmt FROM @bugpk_sql;
EXECUTE bugpk_stmt;
DEALLOCATE PREPARE bugpk_stmt;
