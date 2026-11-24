-- Configurable upstream request timeout.
-- Interface rows use request_timeout_ms=0 as the inherit marker; when
-- request_timeout_override=1 a valid non-zero value takes precedence over the
-- singleton global policy row.

SET @bugpk_schema = DATABASE();

SET @bugpk_add_interface_timeout = IF(EXISTS(
  SELECT 1 FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_schema
    AND TABLE_NAME = 'bp_interface_api'
    AND COLUMN_NAME = 'request_timeout_ms'
), 0, 1);
SET @bugpk_sql = IF(@bugpk_add_interface_timeout = 1,
  'ALTER TABLE `bp_interface_api` ADD COLUMN `request_timeout_ms` int NOT NULL DEFAULT 0 COMMENT ''Upstream timeout in milliseconds; 0 inherits global'' AFTER `delivery_policy_version`',
  'SELECT 1 WHERE 0');
PREPARE bugpk_stmt FROM @bugpk_sql;
EXECUTE bugpk_stmt;
DEALLOCATE PREPARE bugpk_stmt;

SET @bugpk_add_interface_timeout_override = IF(EXISTS(
  SELECT 1 FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_schema
    AND TABLE_NAME = 'bp_interface_api'
    AND COLUMN_NAME = 'request_timeout_override'
), 0, 1);
SET @bugpk_sql = IF(@bugpk_add_interface_timeout_override = 1,
  'ALTER TABLE `bp_interface_api` ADD COLUMN `request_timeout_override` tinyint NOT NULL DEFAULT 0 COMMENT ''1=use interface timeout, 0=inherit global'' AFTER `request_timeout_ms`',
  'SELECT 1 WHERE 0');
PREPARE bugpk_stmt FROM @bugpk_sql;
EXECUTE bugpk_stmt;
DEALLOCATE PREPARE bugpk_stmt;

SET @bugpk_add_global_timeout = IF(EXISTS(
  SELECT 1 FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_schema
    AND TABLE_NAME = 'bp_interface_global_delivery_policy'
    AND COLUMN_NAME = 'request_timeout_ms'
), 0, 1);
SET @bugpk_sql = IF(@bugpk_add_global_timeout = 1,
  'ALTER TABLE `bp_interface_global_delivery_policy` ADD COLUMN `request_timeout_ms` int NOT NULL DEFAULT 15000 COMMENT ''Default upstream timeout in milliseconds'' AFTER `cache_rules`',
  'SELECT 1 WHERE 0');
PREPARE bugpk_stmt FROM @bugpk_sql;
EXECUTE bugpk_stmt;
DEALLOCATE PREPARE bugpk_stmt;

-- Do not overwrite a value an administrator already chose. This only repairs
-- a newly added/legacy zero value and is safe to repeat.
SET @bugpk_sql = IF(@bugpk_add_global_timeout = 1,
  'UPDATE `bp_interface_global_delivery_policy` SET `request_timeout_ms` = 15000 WHERE `id` = 1 AND (`request_timeout_ms` IS NULL OR `request_timeout_ms` <= 0)',
  'SELECT 1 WHERE 0');
PREPARE bugpk_stmt FROM @bugpk_sql;
EXECUTE bugpk_stmt;
DEALLOCATE PREPARE bugpk_stmt;

INSERT INTO `bp_interface_global_delivery_policy`
  (`id`, `request_timeout_ms`)
VALUES (1, 15000)
ON DUPLICATE KEY UPDATE `id` = VALUES(`id`);
