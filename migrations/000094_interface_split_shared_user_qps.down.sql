-- 000094 (rollback): merge the two QPS columns back into a single `qps_limit`.
--
-- Restores the pre-094 schema: move user_qps_limit values into shared_qps_limit,
-- rename the column back to `qps_limit`, then drop the now-redundant
-- user_qps_limit column.  Every step is idempotent so the script is safe to
-- re-run even after a partial or completed rollback.

SET @bugpk_qps_schema = DATABASE();

-- ---------------------------------------------------------------------------
-- Step 1: copy user_qps_limit values back into shared_qps_limit (only while
-- both columns still exist).
-- ---------------------------------------------------------------------------
SET @bugpk_qps_sql = IF(EXISTS(
  SELECT 1 FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_qps_schema
    AND TABLE_NAME = 'bp_interface_api'
    AND COLUMN_NAME = 'shared_qps_limit'
) AND EXISTS(
  SELECT 1 FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_qps_schema
    AND TABLE_NAME = 'bp_interface_api'
    AND COLUMN_NAME = 'user_qps_limit'
), 'UPDATE `bp_interface_api` SET `shared_qps_limit` = `user_qps_limit`', 'SELECT 1 WHERE 0');
PREPARE bugpk_qps_stmt FROM @bugpk_qps_sql;
EXECUTE bugpk_qps_stmt;
DEALLOCATE PREPARE bugpk_qps_stmt;

-- ---------------------------------------------------------------------------
-- Step 2: rename `shared_qps_limit` back to `qps_limit` with the original
-- comment (skip if the rename was already applied).
-- ---------------------------------------------------------------------------
SET @bugpk_qps_sql = IF(EXISTS(
  SELECT 1 FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_qps_schema
    AND TABLE_NAME = 'bp_interface_api'
    AND COLUMN_NAME = 'shared_qps_limit'
), 'ALTER TABLE `bp_interface_api` CHANGE COLUMN `shared_qps_limit` `qps_limit` int NOT NULL DEFAULT 0 COMMENT ''Interface-wide QPS hard limit shared by all users and keys; 0 means unlimited''', 'SELECT 1 WHERE 0');
PREPARE bugpk_qps_stmt FROM @bugpk_qps_sql;
EXECUTE bugpk_qps_stmt;
DEALLOCATE PREPARE bugpk_qps_stmt;

-- ---------------------------------------------------------------------------
-- Step 3: drop the `user_qps_limit` column (idempotent).
-- ---------------------------------------------------------------------------
SET @bugpk_qps_sql = IF(EXISTS(
  SELECT 1 FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_qps_schema
    AND TABLE_NAME = 'bp_interface_api'
    AND COLUMN_NAME = 'user_qps_limit'
), 'ALTER TABLE `bp_interface_api` DROP COLUMN `user_qps_limit`', 'SELECT 1 WHERE 0');
PREPARE bugpk_qps_stmt FROM @bugpk_qps_sql;
EXECUTE bugpk_qps_stmt;
DEALLOCATE PREPARE bugpk_qps_stmt;
