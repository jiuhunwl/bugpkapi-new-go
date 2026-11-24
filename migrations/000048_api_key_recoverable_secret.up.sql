SET @bugpk_schema = DATABASE();
SET @bugpk_sql = IF(EXISTS(
  SELECT 1
  FROM information_schema.COLUMNS
  WHERE TABLE_SCHEMA = @bugpk_schema
    AND TABLE_NAME = 'bp_api_key'
    AND COLUMN_NAME = 'secret_ciphertext'
),
  'SELECT 1 WHERE 0',
  'ALTER TABLE `bp_api_key` ADD COLUMN `secret_ciphertext` varchar(512) DEFAULT NULL COMMENT ''API Key可恢复密文（AES-GCM）'' AFTER `key_hash`'
);
PREPARE bugpk_stmt FROM @bugpk_sql;
EXECUTE bugpk_stmt;
DEALLOCATE PREPARE bugpk_stmt;
