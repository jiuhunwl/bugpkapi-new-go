-- 用户 2FA（TOTP）与恢复码（2026-11-04）。
--
-- bp_user 三列：
--   totp_secret      加密后的 Base32 密钥（secretbox 加密，存储为文本）。
--                    加密密钥复用 systemConfigCipher（与 captcha secret_ref 同路），
--                    明文密钥永不落库。
--   totp_enabled     1 = 已通过「绑定 + 首次验证码确认」，登录/找回密码时强制校验。
--   totp_confirmed_at 确认启用时间，仅用于后台展示与审计。
--
-- bp_user_totp_recovery_code：2FA 启用时生成的一次性恢复码（10 个）。
--   code_hash 存 SHA-256（恢复码属于高熵随机值，无需 bcrypt）；
--   used_at 非空即已消费；登录成功路径原子 UPDATE 抢占，天然防并发重放。
--
-- 全部按「已存在就跳过」的幂等写法：MySQL DDL 不具备事务性，升级可原地重跑。
SET @bugpk_schema = DATABASE();

SET @bugpk_add_totp_secret = IF(EXISTS(
  SELECT 1 FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_schema
    AND TABLE_NAME = 'bp_user'
    AND COLUMN_NAME = 'totp_secret'
), 0, 1);
SET @bugpk_sql = IF(@bugpk_add_totp_secret = 1,
  'ALTER TABLE `bp_user` ADD COLUMN `totp_secret` varchar(255) NULL COMMENT ''TOTP 密钥（secretbox 加密存储）'' AFTER `token_version`',
  'SELECT 1 WHERE 0');
PREPARE bugpk_stmt FROM @bugpk_sql;
EXECUTE bugpk_stmt;
DEALLOCATE PREPARE bugpk_stmt;

SET @bugpk_add_totp_enabled = IF(EXISTS(
  SELECT 1 FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_schema
    AND TABLE_NAME = 'bp_user'
    AND COLUMN_NAME = 'totp_enabled'
), 0, 1);
SET @bugpk_sql = IF(@bugpk_add_totp_enabled = 1,
  'ALTER TABLE `bp_user` ADD COLUMN `totp_enabled` tinyint NOT NULL DEFAULT 0 COMMENT ''2FA 是否启用'' AFTER `totp_secret`, ADD COLUMN `totp_confirmed_at` datetime NULL COMMENT ''2FA 启用确认时间'' AFTER `totp_enabled`',
  'SELECT 1 WHERE 0');
PREPARE bugpk_stmt FROM @bugpk_sql;
EXECUTE bugpk_stmt;
DEALLOCATE PREPARE bugpk_stmt;

CREATE TABLE IF NOT EXISTS `bp_user_totp_recovery_code` (
  `id` bigint NOT NULL AUTO_INCREMENT COMMENT '自增主键',
  `user_id` bigint NOT NULL COMMENT '用户 ID',
  `code_hash` char(64) NOT NULL COMMENT '恢复码 SHA-256 十六进制',
  `used_at` datetime NULL COMMENT '消费时间，NULL=未使用',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '生成时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_user_code_hash` (`user_id`, `code_hash`),
  KEY `idx_totp_recovery_user_used` (`user_id`, `used_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='用户 2FA 一次性恢复码';
