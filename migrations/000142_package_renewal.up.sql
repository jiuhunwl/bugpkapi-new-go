-- 000142: 套餐续费顺延。
--
-- 问题：原购买路径是纯 INSERT（insertGlobalEntitlementTx / BP 接口套餐同款），
-- 用户在有效期内再买一次同一套餐，库里会变成两条各自从 now() 起算的权益。
-- 网关按 end_at asc 先扣最早到期的那张，于是后买的那张在整个前周期内都在排队，
-- 等于白送一段有效期——用户付了两次钱，实际天数没有叠加。
--
-- 本迁移只加两样东西：
--   1. bp_package_renewal_log —— 每次顺延都留一条流水，回滚与退款都靠它算；
--   2. renewal_enabled 开关 —— 体验档/总限量套餐这类不适合顺延的，显式关掉。
-- 权益表本身不动：顺延是对已有行的 end_at 做加法，不改结构。

CREATE TABLE IF NOT EXISTS `bp_package_renewal_log` (
  `id` bigint NOT NULL AUTO_INCREMENT COMMENT '续费流水编号',
  `user_id` bigint NOT NULL COMMENT '用户编号',
  `scope` varchar(16) NOT NULL COMMENT 'GLOBAL / INTERFACE',
  `entitlement_id` bigint NOT NULL COMMENT '被顺延的 bp_user_package_* 记录编号',
  `package_id` bigint NOT NULL DEFAULT 0 COMMENT '套餐编号',
  `spec_id` bigint NOT NULL DEFAULT 0 COMMENT '接口套餐规格编号（全站套餐为 0）',
  `add_days` int NOT NULL COMMENT '本次顺延天数',
  `old_end_at` datetime NOT NULL COMMENT '顺延前到期时间',
  `new_end_at` datetime NOT NULL COMMENT '顺延后到期时间',
  `amount` decimal(12,4) NOT NULL DEFAULT 0.0000 COMMENT '本次续费金额',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  PRIMARY KEY (`id`),
  -- 回滚按钮按 entitlement 反查，退款按 user 反查，两条都要走索引
  KEY `idx_bp_package_renewal_ent` (`scope`, `entitlement_id`, `id`),
  KEY `idx_bp_package_renewal_user` (`user_id`, `created_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='套餐续费顺延流水';

-- 默认全开：绝大多数套餐希望续费是顺延而不是多给一张并行券。
-- 到期时间按「已到期的那天 + 天数」推，不给历史数据留空窗。
ALTER TABLE `bp_package_global`
  ADD COLUMN `renewal_enabled` tinyint NOT NULL DEFAULT 1 COMMENT '1=有效期内重复购买时顺延到期时间，0=每次购买新建一条权益';

ALTER TABLE `bp_package_interface_spec`
  ADD COLUMN `renewal_enabled` tinyint NOT NULL DEFAULT 1 COMMENT '1=有效期内重复购买时顺延到期时间，0=每次购买新建一条权益';
