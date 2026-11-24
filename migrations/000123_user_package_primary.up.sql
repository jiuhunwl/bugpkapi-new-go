-- 用户套餐「主套餐」标记。
--
-- 语义：同一用户全局最多一条 is_primary=1 的权益（两表合计唯一）。
-- 网关扣减顺序 = is_primary DESC（主套餐先扣）→ end_at ASC（快到期的先扣）
--               → daily_limit DESC / qps_limit DESC（同批到期时高配的先扣）→ id ASC。
-- 主套餐耗尽（日限用完 / 到期）后自动回落到默认顺序，不需要用户干预。
--
-- 生产库曾在 1.2.70 期间手工补过这两列与索引（当时 schema_migrations 仍停在 120），
-- 所以本迁移按「列/索引已存在就跳过」的幂等写法执行：直接 ADD COLUMN 会在
-- 已手工补过的库上 1060 duplicate column 报错，把整个升级流程卡在 dirty 状态。
SET @bugpk_schema = DATABASE();

SET @bugpk_add_global_primary = IF(EXISTS(
  SELECT 1 FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_schema
    AND TABLE_NAME = 'bp_user_package_global'
    AND COLUMN_NAME = 'is_primary'
), 0, 1);
SET @bugpk_sql = IF(@bugpk_add_global_primary = 1,
  'ALTER TABLE `bp_user_package_global` ADD COLUMN `is_primary` tinyint NOT NULL DEFAULT 0 COMMENT ''主套餐：1=用户指定的优先扣减套餐'' AFTER `status`',
  'SELECT 1 WHERE 0');
PREPARE bugpk_stmt FROM @bugpk_sql;
EXECUTE bugpk_stmt;
DEALLOCATE PREPARE bugpk_stmt;

SET @bugpk_add_global_primary_idx = IF(EXISTS(
  SELECT 1 FROM information_schema.STATISTICS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_schema
    AND TABLE_NAME = 'bp_user_package_global'
    AND INDEX_NAME = 'idx_bp_user_package_global_primary'
), 0, 1);
SET @bugpk_sql = IF(@bugpk_add_global_primary_idx = 1,
  'ALTER TABLE `bp_user_package_global` ADD KEY `idx_bp_user_package_global_primary` (`user_id`, `is_primary`, `status`, `end_at`)',
  'SELECT 1 WHERE 0');
PREPARE bugpk_stmt FROM @bugpk_sql;
EXECUTE bugpk_stmt;
DEALLOCATE PREPARE bugpk_stmt;

SET @bugpk_add_interface_primary = IF(EXISTS(
  SELECT 1 FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_schema
    AND TABLE_NAME = 'bp_user_package_interface'
    AND COLUMN_NAME = 'is_primary'
), 0, 1);
SET @bugpk_sql = IF(@bugpk_add_interface_primary = 1,
  'ALTER TABLE `bp_user_package_interface` ADD COLUMN `is_primary` tinyint NOT NULL DEFAULT 0 COMMENT ''主套餐：1=用户指定的优先扣减套餐'' AFTER `status`',
  'SELECT 1 WHERE 0');
PREPARE bugpk_stmt FROM @bugpk_sql;
EXECUTE bugpk_stmt;
DEALLOCATE PREPARE bugpk_stmt;

SET @bugpk_add_interface_primary_idx = IF(EXISTS(
  SELECT 1 FROM information_schema.STATISTICS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_schema
    AND TABLE_NAME = 'bp_user_package_interface'
    AND INDEX_NAME = 'idx_bp_user_package_interface_primary'
), 0, 1);
SET @bugpk_sql = IF(@bugpk_add_interface_primary_idx = 1,
  'ALTER TABLE `bp_user_package_interface` ADD KEY `idx_bp_user_package_interface_primary` (`user_id`, `is_primary`, `status`, `end_at`)',
  'SELECT 1 WHERE 0');
PREPARE bugpk_stmt FROM @bugpk_sql;
EXECUTE bugpk_stmt;
DEALLOCATE PREPARE bugpk_stmt;
