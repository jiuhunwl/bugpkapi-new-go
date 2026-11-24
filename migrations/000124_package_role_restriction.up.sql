-- 套餐按用户组售卖（2026-11-04）。
--
-- 平台上的「用户组」即 bp_role，用户通过 bp_user_role / primary_role_id 归属。
-- 三类套餐（全站 / 接口 / 点数）都支持限制购买用户组：
--   role_restricted  = 0 时完全保持原有行为；
--   role_restricted  = 1 时只有 bp_package_role 中的用户组可以购买；
--   role_visible_only = 1 时非允许用户组在套餐商城看不到该套餐（否则仅购买受限）。
-- 未配置任何允许用户组时视为不可购买，管理员保存时由服务层拒绝。
--
-- 三张表的加列都按「列已存在就跳过」执行：MySQL DDL 不是事务性的，
-- 本迁移在三张表中间任意一处失败都会留下「部分表已加列、版本停在 dirty」的状态，
-- 幂等写法让升级可原地重跑，不必人工核对哪张表改过。
SET @bugpk_schema = DATABASE();

SET @bugpk_add_global_role = IF(EXISTS(
  SELECT 1 FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_schema
    AND TABLE_NAME = 'bp_package_global'
    AND COLUMN_NAME = 'role_restricted'
), 0, 1);
SET @bugpk_sql = IF(@bugpk_add_global_role = 1,
  'ALTER TABLE `bp_package_global` ADD COLUMN `role_restricted` tinyint NOT NULL DEFAULT 0 COMMENT ''是否限制用户组购买：0不限制，1限制'' AFTER `sale_end_at`, ADD COLUMN `role_visible_only` tinyint NOT NULL DEFAULT 0 COMMENT ''限制时是否仅对允许用户组可见：0所有人可见，1仅允许组可见'' AFTER `role_restricted`',
  'SELECT 1 WHERE 0');
PREPARE bugpk_stmt FROM @bugpk_sql;
EXECUTE bugpk_stmt;
DEALLOCATE PREPARE bugpk_stmt;

SET @bugpk_add_point_role = IF(EXISTS(
  SELECT 1 FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_schema
    AND TABLE_NAME = 'bp_package_point'
    AND COLUMN_NAME = 'role_restricted'
), 0, 1);
SET @bugpk_sql = IF(@bugpk_add_point_role = 1,
  'ALTER TABLE `bp_package_point` ADD COLUMN `role_restricted` tinyint NOT NULL DEFAULT 0 COMMENT ''是否限制用户组购买：0不限制，1限制'' AFTER `sale_end_at`, ADD COLUMN `role_visible_only` tinyint NOT NULL DEFAULT 0 COMMENT ''限制时是否仅对允许用户组可见：0所有人可见，1仅允许组可见'' AFTER `role_restricted`',
  'SELECT 1 WHERE 0');
PREPARE bugpk_stmt FROM @bugpk_sql;
EXECUTE bugpk_stmt;
DEALLOCATE PREPARE bugpk_stmt;

SET @bugpk_add_interface_role = IF(EXISTS(
  SELECT 1 FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_schema
    AND TABLE_NAME = 'bp_package_interface'
    AND COLUMN_NAME = 'role_restricted'
), 0, 1);
SET @bugpk_sql = IF(@bugpk_add_interface_role = 1,
  'ALTER TABLE `bp_package_interface` ADD COLUMN `role_restricted` tinyint NOT NULL DEFAULT 0 COMMENT ''是否限制用户组购买：0不限制，1限制'' AFTER `remark`, ADD COLUMN `role_visible_only` tinyint NOT NULL DEFAULT 0 COMMENT ''限制时是否仅对允许用户组可见：0所有人可见，1仅允许组可见'' AFTER `role_restricted`',
  'SELECT 1 WHERE 0');
PREPARE bugpk_stmt FROM @bugpk_sql;
EXECUTE bugpk_stmt;
DEALLOCATE PREPARE bugpk_stmt;

CREATE TABLE IF NOT EXISTS `bp_package_role` (
  `package_type` varchar(16) NOT NULL COMMENT '套餐类型：GLOBAL/INTERFACE/POINT',
  `package_id` bigint NOT NULL COMMENT '套餐ID；接口套餐为 bp_package_interface.id',
  `role_id` bigint NOT NULL COMMENT '用户组（角色）ID',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  PRIMARY KEY (`package_type`, `package_id`, `role_id`),
  KEY `idx_bp_package_role_role` (`role_id`),
  CONSTRAINT `chk_bp_package_role_type` CHECK (`package_type` IN ('GLOBAL', 'INTERFACE', 'POINT'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='套餐允许的用户组';
