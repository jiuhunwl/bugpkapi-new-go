-- 回滚用户 2FA（TOTP）与恢复码。
SET @bugpk_schema = DATABASE();

DROP TABLE IF EXISTS `bp_user_totp_recovery_code`;

SET @bugpk_sql = IF(EXISTS(
  SELECT 1 FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_schema
    AND TABLE_NAME = 'bp_user'
    AND COLUMN_NAME = 'totp_enabled'
), 'ALTER TABLE `bp_user` DROP COLUMN `totp_confirmed_at`, DROP COLUMN `totp_enabled`', 'SELECT 1 WHERE 0');
PREPARE bugpk_stmt FROM @bugpk_sql;
EXECUTE bugpk_stmt;
DEALLOCATE PREPARE bugpk_stmt;

SET @bugpk_sql = IF(EXISTS(
  SELECT 1 FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_schema
    AND TABLE_NAME = 'bp_user'
    AND COLUMN_NAME = 'totp_secret'
), 'ALTER TABLE `bp_user` DROP COLUMN `totp_secret`', 'SELECT 1 WHERE 0');
PREPARE bugpk_stmt FROM @bugpk_sql;
EXECUTE bugpk_stmt;
DEALLOCATE PREPARE bugpk_stmt;
