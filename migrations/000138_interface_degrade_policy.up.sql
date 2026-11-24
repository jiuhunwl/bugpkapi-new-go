-- 000138: 接口按用户组次数自动降级。
--
-- 源接口 A 配置一个默认目标接口 B，并为部分用户组设置阈值与计数窗口。
-- 用户在某个窗口内对 A 的已放行请求达到阈值后，后续请求仍调用 A 的网关路径，
-- 但上游执行配置切换为 B；鉴权、计费、配额、限流和调用日志仍按 A 执行。
CREATE TABLE IF NOT EXISTS `bp_interface_degrade_policy` (
  `interface_id` bigint NOT NULL,
  `enabled` tinyint NOT NULL DEFAULT 0 COMMENT '1 = enable degrade for this source interface',
  `default_target_interface_id` bigint DEFAULT NULL COMMENT 'Default target interface; NULL = not configured',
  `policy_version` bigint NOT NULL DEFAULT 1 COMMENT 'Bumped on material changes; resets counters',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`interface_id`),
  KEY `idx_bp_interface_degrade_target` (`default_target_interface_id`),
  CONSTRAINT `chk_bp_interface_degrade_enabled`
    CHECK (`enabled` IN (0, 1)),
  CONSTRAINT `chk_bp_interface_degrade_version`
    CHECK (`policy_version` >= 1)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='Per-interface degrade policy: default target interface';

CREATE TABLE IF NOT EXISTS `bp_interface_role_degrade` (
  `interface_id` bigint NOT NULL,
  `role_id` bigint NOT NULL,
  `count_window` varchar(16) NOT NULL DEFAULT 'day' COMMENT 'day/week/month/total',
  `threshold` bigint NOT NULL DEFAULT 0 COMMENT 'Requests before degrade; 0 = disabled for this role',
  `target_interface_id` bigint DEFAULT NULL COMMENT 'NULL = inherit the interface default target',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`interface_id`, `role_id`),
  KEY `idx_bp_interface_role_degrade_role` (`role_id`),
  KEY `idx_bp_interface_role_degrade_target` (`target_interface_id`),
  CONSTRAINT `chk_bp_interface_role_degrade_window`
    CHECK (`count_window` IN ('day', 'week', 'month', 'total')),
  CONSTRAINT `chk_bp_interface_role_degrade_threshold`
    CHECK (`threshold` >= 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='Per-role thresholds for interface degrade';
