ALTER TABLE `bp_interface_call_log`
  ADD COLUMN `api_key_prefix` varchar(16) DEFAULT NULL COMMENT 'API Key prefix snapshot' AFTER `api_key_id`,
  ADD COLUMN `request_size` bigint NOT NULL DEFAULT 0 COMMENT 'request size bytes' AFTER `request_value`,
  ADD COLUMN `response_size` bigint NOT NULL DEFAULT 0 COMMENT 'response body saved size bytes' AFTER `response_body`,
  ADD COLUMN `ip_country` varchar(64) DEFAULT NULL COMMENT 'IP country' AFTER `client_region`,
  ADD COLUMN `ip_province` varchar(64) DEFAULT NULL COMMENT 'IP province' AFTER `ip_country`,
  ADD COLUMN `ip_city` varchar(64) DEFAULT NULL COMMENT 'IP city' AFTER `ip_province`,
  ADD COLUMN `ip_isp` varchar(128) DEFAULT NULL COMMENT 'IP ISP' AFTER `ip_city`,
  ADD COLUMN `latitude` decimal(10,6) DEFAULT NULL COMMENT 'IP latitude' AFTER `ip_isp`,
  ADD COLUMN `longitude` decimal(10,6) DEFAULT NULL COMMENT 'IP longitude' AFTER `latitude`,
  ADD COLUMN `region_provider` varchar(32) DEFAULT NULL COMMENT 'IP region provider' AFTER `longitude`,
  ADD COLUMN `trace_id` varchar(64) DEFAULT NULL COMMENT 'gateway trace id' AFTER `region_provider`,
  ADD KEY `idx_bp_interface_call_log_region` (`ip_country`, `ip_province`, `ip_city`, `created_at`),
  ADD KEY `idx_bp_interface_call_log_trace` (`trace_id`),
  ADD KEY `idx_bp_interface_call_log_billing_status` (`billing_status`, `created_at`);

CREATE TABLE IF NOT EXISTS `bp_user_log_cleanup_config` (
  `user_id` bigint NOT NULL COMMENT 'user id',
  `retention_days` int NOT NULL DEFAULT 90 COMMENT 'visible log retention days, 0 means keep forever',
  `auto_hide_enabled` tinyint NOT NULL DEFAULT 0 COMMENT 'auto hide old user-visible logs',
  `last_cleanup_at` datetime DEFAULT NULL COMMENT 'last cleanup time',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`user_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='user log cleanup config';

INSERT INTO `bp_menu`
  (`id`, `pid`, `name`, `path`, `component`, `redirect`, `type`, `auth_code`, `meta`, `status`)
VALUES
  (90, 0, 'DashboardStats', '/dashboard-stats', NULL, NULL, 'catalog', 'dashboard:root', JSON_OBJECT('title', '数据看板', 'icon', 'lucide:chart-line', 'order', 9991), 1),
  (91, 90, 'AdminDashboardStats', '/dashboard-stats/admin', '/dashboard/workspace/index', NULL, 'menu', 'dashboard:admin:view', JSON_OBJECT('title', '管理看板', 'order', 1), 1),
  (92, 90, 'UserDashboardStats', '/dashboard-stats/user', '/dashboard/workspace/index', NULL, 'menu', 'dashboard:user:view', JSON_OBJECT('title', '用户看板', 'order', 2), 1),
  (93, 90, 'CallLogDetail', '', NULL, NULL, 'button', 'log:call:detail', JSON_OBJECT('title', '调用日志详情', 'order', 3), 1),
  (94, 90, 'CallLogExport', '', NULL, NULL, 'button', 'log:call:export', JSON_OBJECT('title', '导出调用日志', 'order', 4), 1),
  (95, 90, 'CallLogCleanup', '', NULL, NULL, 'button', 'log:call:cleanup', JSON_OBJECT('title', '清理调用日志', 'order', 5), 1),
  (96, 90, 'CallLogListPermission', '', NULL, NULL, 'button', 'log:call:list', JSON_OBJECT('title', '调用日志列表', 'order', 6), 1)
ON DUPLICATE KEY UPDATE
  `pid` = VALUES(`pid`),
  `path` = VALUES(`path`),
  `component` = VALUES(`component`),
  `redirect` = VALUES(`redirect`),
  `type` = VALUES(`type`),
  `auth_code` = VALUES(`auth_code`),
  `meta` = VALUES(`meta`),
  `status` = VALUES(`status`);

INSERT IGNORE INTO `bp_role_menu` (`role_id`, `menu_id`)
SELECT 1, id FROM `bp_menu` WHERE id BETWEEN 90 AND 96;

INSERT IGNORE INTO `bp_role_menu` (`role_id`, `menu_id`)
SELECT 2, id FROM `bp_menu` WHERE id IN (92, 93);
