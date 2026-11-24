-- Per-interface request parameter validation policy for the open gateway.
-- Rules are evaluated before billing, response caching, and upstream calls.
ALTER TABLE `bp_interface_api`
  ADD COLUMN `request_validation_enabled` tinyint NOT NULL DEFAULT 0 COMMENT 'Whether open-gateway request validation is enabled' AFTER `delivery_policy_version`,
  ADD COLUMN `request_validation_default_result` varchar(32) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'PASS' COMMENT 'PASS/INVALID_PARAMS/PLATFORM_MISMATCH when no rule matches' AFTER `request_validation_enabled`,
  ADD COLUMN `request_validation_rules` json NULL COMMENT 'Ordered request validation rules with ALL/ANY condition logic' AFTER `request_validation_default_result`,
  ADD INDEX `idx_bp_interface_request_validation` (`request_validation_enabled`);

INSERT INTO `bp_menu`
  (`id`,`pid`,`name`,`path`,`component`,`redirect`,`type`,`auth_code`,`meta`,`status`)
VALUES
  (188,41,'InterfaceRequestValidation','',NULL,NULL,'button','interface:request-validation',JSON_OBJECT('title','请求参数检测','order',10),1)
ON DUPLICATE KEY UPDATE
  `pid`=VALUES(`pid`),`type`=VALUES(`type`),`auth_code`=VALUES(`auth_code`),
  `meta`=VALUES(`meta`),`status`=VALUES(`status`);

INSERT IGNORE INTO `bp_role_menu` (`role_id`,`menu_id`) VALUES (1,188);
