-- 000094: split the single interface QPS bucket into two independent limits.
--
-- Previously bp_interface_api had one `qps_limit` column that doubled as the
-- per-user cap and the shared-traffic ceiling.  This migration introduces a
-- dedicated `user_qps_limit` (per-user QPS) and renames the original column to
-- `shared_qps_limit` (global interface capacity), resetting the latter to 0 so
-- the runtime can treat 0 as "unlimited" until operators set an explicit value.
-- Existing qps_limit values are preserved by copying them into user_qps_limit
-- before the rename, maintaining backward-compatible per-user behaviour.

SET @bugpk_qps_schema = DATABASE();

-- ---------------------------------------------------------------------------
-- Step 1: add `user_qps_limit` AFTER `qps_limit` (idempotent).
-- ---------------------------------------------------------------------------
SET @bugpk_qps_sql = IF(EXISTS(
  SELECT 1 FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_qps_schema
    AND TABLE_NAME = 'bp_interface_api'
    AND COLUMN_NAME = 'user_qps_limit'
), 'SELECT 1 WHERE 0',
  'ALTER TABLE `bp_interface_api` ADD COLUMN `user_qps_limit` int NOT NULL DEFAULT 0 COMMENT ''每个用户调用该接口的最高 QPS；0 表示不限制'' AFTER `qps_limit`');
PREPARE bugpk_qps_stmt FROM @bugpk_qps_sql;
EXECUTE bugpk_qps_stmt;
DEALLOCATE PREPARE bugpk_qps_stmt;

-- ---------------------------------------------------------------------------
-- Step 2: copy old qps_limit values into user_qps_limit (only while the
-- original column still exists — before the rename).
-- ---------------------------------------------------------------------------
SET @bugpk_qps_sql = IF(EXISTS(
  SELECT 1 FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_qps_schema
    AND TABLE_NAME = 'bp_interface_api'
    AND COLUMN_NAME = 'qps_limit'
), 'UPDATE `bp_interface_api` SET `user_qps_limit` = `qps_limit`', 'SELECT 1 WHERE 0');
PREPARE bugpk_qps_stmt FROM @bugpk_qps_sql;
EXECUTE bugpk_qps_stmt;
DEALLOCATE PREPARE bugpk_qps_stmt;

-- ---------------------------------------------------------------------------
-- Step 3: rename `qps_limit` → `shared_qps_limit` with an updated comment
-- (skip if the rename was already applied).
-- ---------------------------------------------------------------------------
SET @bugpk_qps_sql = IF(EXISTS(
  SELECT 1 FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_qps_schema
    AND TABLE_NAME = 'bp_interface_api'
    AND COLUMN_NAME = 'shared_qps_limit'
), 'SELECT 1 WHERE 0',
  'ALTER TABLE `bp_interface_api` CHANGE COLUMN `qps_limit` `shared_qps_limit` int NOT NULL DEFAULT 0 COMMENT ''所有用户共享的接口总容量 QPS；0 表示不限制''');
PREPARE bugpk_qps_stmt FROM @bugpk_qps_sql;
EXECUTE bugpk_qps_stmt;
DEALLOCATE PREPARE bugpk_qps_stmt;

-- ---------------------------------------------------------------------------
-- Step 4: reset shared_qps_limit to 0 — the legacy value now lives in
-- user_qps_limit, so the shared ceiling defaults to "unlimited".
-- ---------------------------------------------------------------------------
UPDATE `bp_interface_api` SET `shared_qps_limit` = 0;
