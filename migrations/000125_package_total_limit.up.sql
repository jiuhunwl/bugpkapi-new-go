-- 套餐总调用量 + 计量方式（2026-11-04）。
--
-- total_limit：套餐生命周期内的总调用量上限，0 = 不限。
--   模板表（bp_package_global / bp_package_interface_spec）配置，
--   发放到 bp_user_package_global / bp_user_package_interface 时快照；
--   网关扣减与日限同一事务，任一上限打满即不再消耗该权益。
--   与 daily_limit 同时配置时要求 total_limit >= daily_limit（服务层校验），
--   否则日限永远先于总量触顶，总量配置成为死配置。
--
-- count_mode：该套餐的计量口径，发行时快照到权益行：
--   success = 仅成功计量（默认，保持既有行为：预留 → 失败释放时回退计数）；
--   attempt = 失败也计量（失败释放时不回退 day_used/total_used，
--             计费记录仍置 RELEASED，仅计数不退）。
--
-- 四张表的加列都按「列已存在就跳过」执行：MySQL DDL 不是事务性的，
-- 幂等写法让升级可原地重跑。
SET @bugpk_schema = DATABASE();

SET @bugpk_add_pkg_global = IF(EXISTS(
  SELECT 1 FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_schema
    AND TABLE_NAME = 'bp_package_global'
    AND COLUMN_NAME = 'total_limit'
), 0, 1);
SET @bugpk_sql = IF(@bugpk_add_pkg_global = 1,
  'ALTER TABLE `bp_package_global` ADD COLUMN `total_limit` bigint NOT NULL DEFAULT 0 COMMENT ''总调用量上限，0不限'' AFTER `daily_limit`, ADD COLUMN `count_mode` varchar(16) NOT NULL DEFAULT ''success'' COMMENT ''计量方式：success=仅成功计量，attempt=失败也计量'' AFTER `total_limit`',
  'SELECT 1 WHERE 0');
PREPARE bugpk_stmt FROM @bugpk_sql;
EXECUTE bugpk_stmt;
DEALLOCATE PREPARE bugpk_stmt;

SET @bugpk_add_pkg_spec = IF(EXISTS(
  SELECT 1 FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_schema
    AND TABLE_NAME = 'bp_package_interface_spec'
    AND COLUMN_NAME = 'total_limit'
), 0, 1);
SET @bugpk_sql = IF(@bugpk_add_pkg_spec = 1,
  'ALTER TABLE `bp_package_interface_spec` ADD COLUMN `total_limit` bigint NOT NULL DEFAULT 0 COMMENT ''总调用量上限，0不限'' AFTER `daily_limit`, ADD COLUMN `count_mode` varchar(16) NOT NULL DEFAULT ''success'' COMMENT ''计量方式：success=仅成功计量，attempt=失败也计量'' AFTER `total_limit`',
  'SELECT 1 WHERE 0');
PREPARE bugpk_stmt FROM @bugpk_sql;
EXECUTE bugpk_stmt;
DEALLOCATE PREPARE bugpk_stmt;

SET @bugpk_add_user_global = IF(EXISTS(
  SELECT 1 FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_schema
    AND TABLE_NAME = 'bp_user_package_global'
    AND COLUMN_NAME = 'total_limit'
), 0, 1);
SET @bugpk_sql = IF(@bugpk_add_user_global = 1,
  'ALTER TABLE `bp_user_package_global` ADD COLUMN `total_limit` bigint NOT NULL DEFAULT 0 COMMENT ''总调用量上限快照，0不限'' AFTER `daily_limit`, ADD COLUMN `count_mode` varchar(16) NOT NULL DEFAULT ''success'' COMMENT ''计量方式快照：success=仅成功计量，attempt=失败也计量'' AFTER `total_limit`',
  'SELECT 1 WHERE 0');
PREPARE bugpk_stmt FROM @bugpk_sql;
EXECUTE bugpk_stmt;
DEALLOCATE PREPARE bugpk_stmt;

SET @bugpk_add_user_interface = IF(EXISTS(
  SELECT 1 FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_schema
    AND TABLE_NAME = 'bp_user_package_interface'
    AND COLUMN_NAME = 'total_limit'
), 0, 1);
SET @bugpk_sql = IF(@bugpk_add_user_interface = 1,
  'ALTER TABLE `bp_user_package_interface` ADD COLUMN `total_limit` bigint NOT NULL DEFAULT 0 COMMENT ''总调用量上限快照，0不限'' AFTER `daily_limit`, ADD COLUMN `count_mode` varchar(16) NOT NULL DEFAULT ''success'' COMMENT ''计量方式快照：success=仅成功计量，attempt=失败也计量'' AFTER `total_limit`',
  'SELECT 1 WHERE 0');
PREPARE bugpk_stmt FROM @bugpk_sql;
EXECUTE bugpk_stmt;
DEALLOCATE PREPARE bugpk_stmt;
