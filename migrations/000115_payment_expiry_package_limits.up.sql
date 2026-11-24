-- 支付订单时效与套餐限量：
-- 1) 订单增加库存占用状态，用于限量套餐预占/释放/成交追溯；
-- 2) 套餐增加限量份数、每人限购与售卖时间窗口；
-- 3) 增加待支付订单复用与过期扫描索引。

ALTER TABLE `bp_package_global`
  ADD COLUMN `stock_limit` int NOT NULL DEFAULT 0 COMMENT '限量份数，0不限' AFTER `qps_limit`,
  ADD COLUMN `stock_used` int NOT NULL DEFAULT 0 COMMENT '已占用份数（含待支付预占）' AFTER `stock_limit`,
  ADD COLUMN `per_user_limit` int NOT NULL DEFAULT 0 COMMENT '每人限购份数，0不限' AFTER `stock_used`,
  ADD COLUMN `sale_start_at` datetime DEFAULT NULL COMMENT '开售时间，空表示不限' AFTER `per_user_limit`,
  ADD COLUMN `sale_end_at` datetime DEFAULT NULL COMMENT '停售时间，空表示不限' AFTER `sale_start_at`,
  ADD KEY `idx_bp_package_global_sale` (`status`, `sale_start_at`, `sale_end_at`);

ALTER TABLE `bp_package_point`
  ADD COLUMN `stock_limit` int NOT NULL DEFAULT 0 COMMENT '限量份数，0不限' AFTER `points`,
  ADD COLUMN `stock_used` int NOT NULL DEFAULT 0 COMMENT '已占用份数（含待支付预占）' AFTER `stock_limit`,
  ADD COLUMN `per_user_limit` int NOT NULL DEFAULT 0 COMMENT '每人限购份数，0不限' AFTER `stock_used`,
  ADD COLUMN `sale_start_at` datetime DEFAULT NULL COMMENT '开售时间，空表示不限' AFTER `per_user_limit`,
  ADD COLUMN `sale_end_at` datetime DEFAULT NULL COMMENT '停售时间，空表示不限' AFTER `sale_start_at`,
  ADD KEY `idx_bp_package_point_sale` (`status`, `sale_start_at`, `sale_end_at`);

ALTER TABLE `bp_package_interface_spec`
  ADD COLUMN `stock_limit` int NOT NULL DEFAULT 0 COMMENT '限量份数，0不限' AFTER `qps_limit`,
  ADD COLUMN `stock_used` int NOT NULL DEFAULT 0 COMMENT '已占用份数（含待支付预占）' AFTER `stock_limit`,
  ADD COLUMN `per_user_limit` int NOT NULL DEFAULT 0 COMMENT '每人限购份数，0不限' AFTER `stock_used`,
  ADD COLUMN `sale_start_at` datetime DEFAULT NULL COMMENT '开售时间，空表示不限' AFTER `per_user_limit`,
  ADD COLUMN `sale_end_at` datetime DEFAULT NULL COMMENT '停售时间，空表示不限' AFTER `sale_start_at`,
  ADD KEY `idx_bp_package_interface_spec_sale` (`status`, `sale_start_at`, `sale_end_at`);

ALTER TABLE `bp_payment_order`
  ADD COLUMN `stock_reserved` tinyint NOT NULL DEFAULT 0 COMMENT '库存占用状态：0不占用,1占用中,2已释放,3已成交' AFTER `grant_points`,
  ADD KEY `idx_bp_payment_order_reuse` (`user_id`, `order_type`, `target_id`, `status`, `id`),
  ADD KEY `idx_bp_payment_order_expire` (`status`, `expire_at`);
