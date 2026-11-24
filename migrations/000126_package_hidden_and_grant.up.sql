-- 套餐隐藏开关 + 管理端下发套餐（2026-11-04）。
--
-- hidden = 1 的套餐在商城（/package/mall/*）对所有人不可见也不可购买，
-- 管理端列表照常可见，可通过「下发套餐」接口直接把权益发给指定用户。
-- 隐藏只影响售卖面，不影响已发放权益的扣减与展示。
--
-- 加列按「列已存在就跳过」执行：MySQL DDL 不是事务性的，
-- 幂等写法让升级可原地重跑（与 000124 同款模板）。
SET @bugpk_schema = DATABASE();

SET @bugpk_add_global_hidden = IF(EXISTS(
  SELECT 1 FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_schema
    AND TABLE_NAME = 'bp_package_global'
    AND COLUMN_NAME = 'hidden'
), 0, 1);
SET @bugpk_sql = IF(@bugpk_add_global_hidden = 1,
  'ALTER TABLE `bp_package_global` ADD COLUMN `hidden` tinyint NOT NULL DEFAULT 0 COMMENT ''商城隐藏：0正常售卖，1商城隐藏且不可购买，仅管理端可下发'' AFTER `role_visible_only`',
  'SELECT 1 WHERE 0');
PREPARE bugpk_stmt FROM @bugpk_sql;
EXECUTE bugpk_stmt;
DEALLOCATE PREPARE bugpk_stmt;

SET @bugpk_add_point_hidden = IF(EXISTS(
  SELECT 1 FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_schema
    AND TABLE_NAME = 'bp_package_point'
    AND COLUMN_NAME = 'hidden'
), 0, 1);
SET @bugpk_sql = IF(@bugpk_add_point_hidden = 1,
  'ALTER TABLE `bp_package_point` ADD COLUMN `hidden` tinyint NOT NULL DEFAULT 0 COMMENT ''商城隐藏：0正常售卖，1商城隐藏且不可购买，仅管理端可下发'' AFTER `role_visible_only`',
  'SELECT 1 WHERE 0');
PREPARE bugpk_stmt FROM @bugpk_sql;
EXECUTE bugpk_stmt;
DEALLOCATE PREPARE bugpk_stmt;

SET @bugpk_add_interface_hidden = IF(EXISTS(
  SELECT 1 FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_schema
    AND TABLE_NAME = 'bp_package_interface'
    AND COLUMN_NAME = 'hidden'
), 0, 1);
SET @bugpk_sql = IF(@bugpk_add_interface_hidden = 1,
  'ALTER TABLE `bp_package_interface` ADD COLUMN `hidden` tinyint NOT NULL DEFAULT 0 COMMENT ''商城隐藏：0正常售卖，1商城隐藏且不可购买，仅管理端可下发（对全部规格生效）'' AFTER `role_visible_only`',
  'SELECT 1 WHERE 0');
PREPARE bugpk_stmt FROM @bugpk_sql;
EXECUTE bugpk_stmt;
DEALLOCATE PREPARE bugpk_stmt;

-- 按钮权限：向用户下发套餐（挂在「用户套餐」菜单 243 下，246 空闲）
INSERT INTO `bp_menu`
  (`id`, `pid`, `name`, `path`, `component`, `redirect`, `type`, `auth_code`, `meta`, `status`)
VALUES
  (246, 243, 'UserPackageGrant', '', NULL, NULL, 'button', 'package:user:grant', JSON_OBJECT('title', 'system.package.userGrantAction'), 1)
ON DUPLICATE KEY UPDATE
  `pid` = VALUES(`pid`),
  `type` = VALUES(`type`),
  `auth_code` = VALUES(`auth_code`),
  `meta` = VALUES(`meta`),
  `status` = VALUES(`status`);

INSERT IGNORE INTO `bp_role_menu` (`role_id`, `menu_id`)
SELECT 1, `id` FROM `bp_menu` WHERE `id` = 246;
