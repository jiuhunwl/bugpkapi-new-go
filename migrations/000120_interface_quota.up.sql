-- 单接口维度的小时 / 天 / 周 / 月配额（2026-11-03）。
--
-- 与 bp_role 的「用户组总量配额」互补：
--   * bp_role 限制的是「一个账号在平台内的总调用量」；
--   * bp_interface_quota 限制的是「一个接口的单用户调用量」和「该接口的全平台
--     总调用量」，用于保护单个上游、控制单接口成本。
--
-- 0 表示不限制；enabled=0 时该接口不启用本层配额，完全保持原有行为。
-- count_mode 独立于用户组计量：attempt = 进入闸门即计（失败也计），
-- success = 成功返回后补记。这样即使某个用户组按成功计量，接口侧仍可
-- 选择更严格的「失败也计」。
CREATE TABLE IF NOT EXISTS `bp_interface_quota` (
  `interface_id` bigint NOT NULL,
  `enabled` tinyint NOT NULL DEFAULT 0 COMMENT '1 = enforce interface quota',
  `count_mode` varchar(16) NOT NULL DEFAULT 'attempt' COMMENT 'attempt/success',
  `per_user_hour` bigint NOT NULL DEFAULT 0 COMMENT 'Per-user cap per hour; 0 = unlimited',
  `per_user_day` bigint NOT NULL DEFAULT 0 COMMENT 'Per-user cap per day; 0 = unlimited',
  `per_user_week` bigint NOT NULL DEFAULT 0 COMMENT 'Per-user cap per ISO week; 0 = unlimited',
  `per_user_month` bigint NOT NULL DEFAULT 0 COMMENT 'Per-user cap per calendar month; 0 = unlimited',
  `all_users_hour` bigint NOT NULL DEFAULT 0 COMMENT 'Interface-wide cap per hour; 0 = unlimited',
  `all_users_day` bigint NOT NULL DEFAULT 0 COMMENT 'Interface-wide cap per day; 0 = unlimited',
  `all_users_week` bigint NOT NULL DEFAULT 0 COMMENT 'Interface-wide cap per ISO week; 0 = unlimited',
  `all_users_month` bigint NOT NULL DEFAULT 0 COMMENT 'Interface-wide cap per calendar month; 0 = unlimited',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`interface_id`),
  CONSTRAINT `chk_bp_interface_quota_count_mode`
    CHECK (`count_mode` IN ('attempt', 'success')),
  CONSTRAINT `chk_bp_interface_quota_non_negative`
    CHECK (`per_user_hour` >= 0 AND `per_user_day` >= 0 AND `per_user_week` >= 0 AND `per_user_month` >= 0
           AND `all_users_hour` >= 0 AND `all_users_day` >= 0 AND `all_users_week` >= 0 AND `all_users_month` >= 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='Per-interface user and platform request quotas';

-- 用户组对「单接口单用户上限」的覆盖：
--   inherit   = 使用接口默认单用户上限；
--   unlimited = 该组不受单用户上限限制（接口全平台总量仍然生效）；
--   adjust    = 在接口默认值上加减（delta 可为负；默认值 0=不限时保持不限）；
--   override  = 直接使用本行的绝对值（0=不限）。
-- 这是「按用户组灵活配置」的唯一入口；全平台总量不支持按用户组改。
CREATE TABLE IF NOT EXISTS `bp_interface_role_quota` (
  `interface_id` bigint NOT NULL,
  `role_id` bigint NOT NULL,
  `mode` varchar(16) NOT NULL DEFAULT 'inherit' COMMENT 'inherit/unlimited/adjust/override',
  `adjust_hour` bigint NOT NULL DEFAULT 0 COMMENT 'Signed delta applied to per_user_hour',
  `adjust_day` bigint NOT NULL DEFAULT 0 COMMENT 'Signed delta applied to per_user_day',
  `adjust_week` bigint NOT NULL DEFAULT 0 COMMENT 'Signed delta applied to per_user_week',
  `adjust_month` bigint NOT NULL DEFAULT 0 COMMENT 'Signed delta applied to per_user_month',
  `override_hour` bigint NOT NULL DEFAULT 0 COMMENT 'Absolute per-user cap per hour; 0 = unlimited',
  `override_day` bigint NOT NULL DEFAULT 0 COMMENT 'Absolute per-user cap per day; 0 = unlimited',
  `override_week` bigint NOT NULL DEFAULT 0 COMMENT 'Absolute per-user cap per ISO week; 0 = unlimited',
  `override_month` bigint NOT NULL DEFAULT 0 COMMENT 'Absolute per-user cap per calendar month; 0 = unlimited',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`interface_id`, `role_id`),
  KEY `idx_bp_interface_role_quota_role` (`role_id`),
  CONSTRAINT `chk_bp_interface_role_quota_mode`
    CHECK (`mode` IN ('inherit', 'unlimited', 'adjust', 'override')),
  CONSTRAINT `chk_bp_interface_role_quota_override_non_negative`
    CHECK (`override_hour` >= 0 AND `override_day` >= 0 AND `override_week` >= 0 AND `override_month` >= 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='Per-role overrides for per-interface per-user quotas';
