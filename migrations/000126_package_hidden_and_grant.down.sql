-- 回滚：去掉 hidden 列与下发套餐按钮权限。
SET @bugpk_schema = DATABASE();

DELETE FROM `bp_role_menu` WHERE `menu_id` = 246;
DELETE FROM `bp_menu` WHERE `id` = 246 AND `auth_code` = 'package:user:grant';

SET @bugpk_drop_global_hidden = IF(EXISTS(
  SELECT 1 FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_schema
    AND TABLE_NAME = 'bp_package_global'
    AND COLUMN_NAME = 'hidden'
), 1, 0);
SET @bugpk_sql = IF(@bugpk_drop_global_hidden = 1,
  'ALTER TABLE `bp_package_global` DROP COLUMN `hidden`',
  'SELECT 1 WHERE 0');
PREPARE bugpk_stmt FROM @bugpk_sql;
EXECUTE bugpk_stmt;
DEALLOCATE PREPARE bugpk_stmt;

SET @bugpk_drop_point_hidden = IF(EXISTS(
  SELECT 1 FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_schema
    AND TABLE_NAME = 'bp_package_point'
    AND COLUMN_NAME = 'hidden'
), 1, 0);
SET @bugpk_sql = IF(@bugpk_drop_point_hidden = 1,
  'ALTER TABLE `bp_package_point` DROP COLUMN `hidden`',
  'SELECT 1 WHERE 0');
PREPARE bugpk_stmt FROM @bugpk_sql;
EXECUTE bugpk_stmt;
DEALLOCATE PREPARE bugpk_stmt;

SET @bugpk_drop_interface_hidden = IF(EXISTS(
  SELECT 1 FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_schema
    AND TABLE_NAME = 'bp_package_interface'
    AND COLUMN_NAME = 'hidden'
), 1, 0);
SET @bugpk_sql = IF(@bugpk_drop_interface_hidden = 1,
  'ALTER TABLE `bp_package_interface` DROP COLUMN `hidden`',
  'SELECT 1 WHERE 0');
PREPARE bugpk_stmt FROM @bugpk_sql;
EXECUTE bugpk_stmt;
DEALLOCATE PREPARE bugpk_stmt;
