CREATE TABLE IF NOT EXISTS `bp_package_global` (
  `id` bigint NOT NULL AUTO_INCREMENT COMMENT '全站套餐编号',
  `name` varchar(128) NOT NULL COMMENT '套餐名称',
  `price` decimal(12,4) NOT NULL DEFAULT 0.0000 COMMENT '购买价格',
  `valid_days` int NOT NULL DEFAULT 30 COMMENT '有效天数',
  `daily_limit` bigint NOT NULL DEFAULT 0 COMMENT '每日调用上限，0不限',
  `qps_limit` int NOT NULL DEFAULT 0 COMMENT 'QPS上限，0不限',
  `status` tinyint NOT NULL DEFAULT 1 COMMENT '状态：1启用，0禁用',
  `remark` varchar(255) DEFAULT NULL COMMENT '备注',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  KEY `idx_bp_package_global_status` (`status`, `price`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='全站套餐表';

CREATE TABLE IF NOT EXISTS `bp_package_interface` (
  `id` bigint NOT NULL AUTO_INCREMENT COMMENT '接口套餐编号',
  `interface_id` bigint NOT NULL COMMENT '接口编号',
  `name` varchar(128) NOT NULL COMMENT '套餐名称',
  `status` tinyint NOT NULL DEFAULT 1 COMMENT '状态：1启用，0禁用',
  `remark` varchar(255) DEFAULT NULL COMMENT '备注',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  KEY `idx_bp_package_interface_api` (`interface_id`, `status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='接口套餐主表';

CREATE TABLE IF NOT EXISTS `bp_package_interface_spec` (
  `id` bigint NOT NULL AUTO_INCREMENT COMMENT '接口套餐规格编号',
  `package_id` bigint NOT NULL COMMENT '接口套餐编号',
  `name` varchar(128) NOT NULL COMMENT '规格名称',
  `price` decimal(12,4) NOT NULL DEFAULT 0.0000 COMMENT '购买价格',
  `valid_days` int NOT NULL DEFAULT 30 COMMENT '有效天数',
  `daily_limit` bigint NOT NULL DEFAULT 0 COMMENT '每日调用上限，0不限',
  `qps_limit` int NOT NULL DEFAULT 0 COMMENT 'QPS上限，0不限',
  `status` tinyint NOT NULL DEFAULT 1 COMMENT '状态：1启用，0禁用',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  KEY `idx_bp_package_interface_spec_pkg` (`package_id`, `status`, `price`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='接口套餐规格表';

CREATE TABLE IF NOT EXISTS `bp_package_point` (
  `id` bigint NOT NULL AUTO_INCREMENT COMMENT '点数套餐编号',
  `name` varchar(128) NOT NULL COMMENT '套餐名称',
  `price` decimal(12,4) NOT NULL DEFAULT 0.0000 COMMENT '购买价格',
  `points` bigint NOT NULL DEFAULT 0 COMMENT '购买点数',
  `status` tinyint NOT NULL DEFAULT 1 COMMENT '状态：1启用，0禁用',
  `remark` varchar(255) DEFAULT NULL COMMENT '备注',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  KEY `idx_bp_package_point_status` (`status`, `price`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='点数套餐表';

CREATE TABLE IF NOT EXISTS `bp_user_package_global` (
  `id` bigint NOT NULL AUTO_INCREMENT COMMENT '用户全站套餐权益编号',
  `user_id` bigint NOT NULL COMMENT '用户编号',
  `package_id` bigint NOT NULL COMMENT '购买套餐编号',
  `package_name` varchar(128) NOT NULL COMMENT '套餐名称快照',
  `daily_limit` bigint NOT NULL DEFAULT 0 COMMENT '每日调用上限快照，0不限',
  `qps_limit` int NOT NULL DEFAULT 0 COMMENT 'QPS上限快照，0不限',
  `start_at` datetime NOT NULL COMMENT '开始时间',
  `end_at` datetime NOT NULL COMMENT '结束时间',
  `day_key` date DEFAULT NULL COMMENT '当前统计日期',
  `day_used` bigint NOT NULL DEFAULT 0 COMMENT '当日已用',
  `total_used` bigint NOT NULL DEFAULT 0 COMMENT '总已用',
  `status` tinyint NOT NULL DEFAULT 1 COMMENT '状态：1启用，0禁用',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  KEY `idx_bp_user_package_global_available` (`user_id`, `status`, `end_at`, `id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='用户全站套餐权益表';

CREATE TABLE IF NOT EXISTS `bp_user_package_interface` (
  `id` bigint NOT NULL AUTO_INCREMENT COMMENT '用户接口套餐权益编号',
  `user_id` bigint NOT NULL COMMENT '用户编号',
  `interface_id` bigint NOT NULL COMMENT '接口编号',
  `package_id` bigint NOT NULL COMMENT '接口套餐编号',
  `spec_id` bigint NOT NULL COMMENT '接口套餐规格编号',
  `package_name` varchar(128) NOT NULL COMMENT '套餐名称快照',
  `spec_name` varchar(128) NOT NULL COMMENT '规格名称快照',
  `daily_limit` bigint NOT NULL DEFAULT 0 COMMENT '每日调用上限快照，0不限',
  `qps_limit` int NOT NULL DEFAULT 0 COMMENT 'QPS上限快照，0不限',
  `start_at` datetime NOT NULL COMMENT '开始时间',
  `end_at` datetime NOT NULL COMMENT '结束时间',
  `day_key` date DEFAULT NULL COMMENT '当前统计日期',
  `day_used` bigint NOT NULL DEFAULT 0 COMMENT '当日已用',
  `total_used` bigint NOT NULL DEFAULT 0 COMMENT '总已用',
  `status` tinyint NOT NULL DEFAULT 1 COMMENT '状态：1启用，0禁用',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  KEY `idx_bp_user_package_interface_available` (`user_id`, `interface_id`, `status`, `end_at`, `id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='用户接口套餐权益表';

CREATE TABLE IF NOT EXISTS `bp_billing_record` (
  `id` bigint NOT NULL AUTO_INCREMENT COMMENT '计费记录编号',
  `call_log_id` bigint DEFAULT NULL COMMENT '调用日志编号',
  `user_id` bigint NOT NULL COMMENT '用户编号',
  `api_key_id` bigint DEFAULT NULL COMMENT 'API Key编号',
  `interface_id` bigint NOT NULL COMMENT '接口编号',
  `api_code` varchar(64) NOT NULL COMMENT '接口编码快照',
  `charge_type` varchar(32) NOT NULL COMMENT '扣费类型',
  `charge_amount` decimal(12,4) NOT NULL DEFAULT 0.0000 COMMENT '余额扣费金额',
  `point_amount` bigint NOT NULL DEFAULT 0 COMMENT '点数扣费数量',
  `package_record_id` bigint DEFAULT NULL COMMENT '套餐权益记录编号',
  `status` varchar(32) NOT NULL DEFAULT 'RESERVED' COMMENT 'RESERVED/CONFIRMED/RELEASED/FAILED',
  `reason` varchar(255) DEFAULT NULL COMMENT '原因',
  `snapshot` json DEFAULT NULL COMMENT '计费快照',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  KEY `idx_bp_billing_record_user` (`user_id`, `created_at`),
  KEY `idx_bp_billing_record_call` (`call_log_id`),
  KEY `idx_bp_billing_record_status` (`status`, `created_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='开放调用计费记录表';

CREATE TABLE IF NOT EXISTS `bp_wallet_transaction` (
  `id` bigint NOT NULL AUTO_INCREMENT COMMENT '钱包流水编号',
  `user_id` bigint NOT NULL COMMENT '用户编号',
  `biz_type` varchar(32) NOT NULL COMMENT '业务类型',
  `biz_id` bigint DEFAULT NULL COMMENT '业务编号',
  `amount` decimal(12,4) NOT NULL DEFAULT 0.0000 COMMENT '余额变动',
  `points` bigint NOT NULL DEFAULT 0 COMMENT '点数变动',
  `balance_after` decimal(12,4) NOT NULL DEFAULT 0.0000 COMMENT '变动后余额',
  `points_after` bigint NOT NULL DEFAULT 0 COMMENT '变动后点数',
  `remark` varchar(255) DEFAULT NULL COMMENT '备注',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  KEY `idx_bp_wallet_transaction_user` (`user_id`, `created_at`),
  KEY `idx_bp_wallet_transaction_biz` (`biz_type`, `biz_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='用户余额点数流水表';

ALTER TABLE `bp_interface_call_log`
  ADD COLUMN `billing_record_id` bigint DEFAULT NULL COMMENT '计费记录编号' AFTER `billing_snapshot`,
  ADD KEY `idx_bp_interface_call_log_billing_record` (`billing_record_id`);

INSERT INTO `bp_menu`
  (`id`, `pid`, `name`, `path`, `component`, `redirect`, `type`, `auth_code`, `meta`, `status`)
VALUES
  (70, 0, 'PackageManagement', '/package', NULL, NULL, 'catalog', 'package:root', JSON_OBJECT('title', 'system.package.title', 'icon', 'lucide:boxes', 'order', 9993), 1),
  (71, 70, 'GlobalPackageList', '/package/global', '/package/global/list', NULL, 'menu', 'package:global:list', JSON_OBJECT('title', '全站套餐', 'order', 1), 1),
  (72, 71, 'GlobalPackageCreate', '', NULL, NULL, 'button', 'package:global:create', JSON_OBJECT('title', '新增全站套餐'), 1),
  (73, 71, 'GlobalPackageUpdate', '', NULL, NULL, 'button', 'package:global:update', JSON_OBJECT('title', '编辑全站套餐'), 1),
  (74, 71, 'GlobalPackageDelete', '', NULL, NULL, 'button', 'package:global:delete', JSON_OBJECT('title', '删除全站套餐'), 1),
  (75, 70, 'InterfacePackageList', '/package/interface', '/package/interface/list', NULL, 'menu', 'package:interface:list', JSON_OBJECT('title', '接口套餐', 'order', 2), 1),
  (76, 75, 'InterfacePackageCreate', '', NULL, NULL, 'button', 'package:interface:create', JSON_OBJECT('title', '新增接口套餐'), 1),
  (77, 75, 'InterfacePackageUpdate', '', NULL, NULL, 'button', 'package:interface:update', JSON_OBJECT('title', '编辑接口套餐'), 1),
  (78, 75, 'InterfacePackageDelete', '', NULL, NULL, 'button', 'package:interface:delete', JSON_OBJECT('title', '删除接口套餐'), 1),
  (79, 70, 'PointPackageList', '/package/point', '/package/point/list', NULL, 'menu', 'package:point:list', JSON_OBJECT('title', '点数套餐', 'order', 3), 1),
  (80, 79, 'PointPackageCreate', '', NULL, NULL, 'button', 'package:point:create', JSON_OBJECT('title', '新增点数套餐'), 1),
  (81, 79, 'PointPackageUpdate', '', NULL, NULL, 'button', 'package:point:update', JSON_OBJECT('title', '编辑点数套餐'), 1),
  (82, 79, 'PointPackageDelete', '', NULL, NULL, 'button', 'package:point:delete', JSON_OBJECT('title', '删除点数套餐'), 1),
  (83, 70, 'BillingRecordList', '/package/billing-records', '/interface/log', NULL, 'menu', 'billing:record:list', JSON_OBJECT('title', '计费记录', 'order', 4), 1),
  (84, 0, 'PackageMall', '/package/mall', NULL, NULL, 'catalog', 'package:mall:list', JSON_OBJECT('title', '套餐商城', 'icon', 'lucide:shopping-bag', 'order', 9992), 1),
  (85, 84, 'PackageMallPurchase', '', NULL, NULL, 'button', 'package:mall:purchase', JSON_OBJECT('title', '购买套餐'), 1)
ON DUPLICATE KEY UPDATE
  `pid` = VALUES(`pid`),
  `path` = VALUES(`path`),
  `component` = VALUES(`component`),
  `type` = VALUES(`type`),
  `auth_code` = VALUES(`auth_code`),
  `meta` = VALUES(`meta`),
  `status` = VALUES(`status`);

INSERT INTO `bp_menu`
  (`id`, `pid`, `name`, `path`, `component`, `redirect`, `type`, `auth_code`, `meta`, `status`)
VALUES
  (86, 84, 'UserGlobalPackage', '/purchase/global', '/purchase/global/list', NULL, 'menu', 'package:mall:list', JSON_OBJECT('title', 'system.package.buyGlobalTitle', 'icon', 'mdi:shopping-outline', 'order', 1), 1),
  (87, 84, 'UserInterfacePackage', '/purchase/interface', '/purchase/interface/list', NULL, 'menu', 'package:mall:list', JSON_OBJECT('title', 'system.package.buyInterfaceTitle', 'icon', 'mdi:shopping-outline', 'order', 2), 1),
  (88, 84, 'UserPointPackage', '/purchase/point', '/purchase/point/list', NULL, 'menu', 'package:mall:list', JSON_OBJECT('title', 'system.package.buyPointTitle', 'icon', 'mdi:database-plus', 'order', 3), 1),
  (89, 84, 'UserPaymentOrder', '/purchase/orders', '/payment/order/user', NULL, 'menu', 'package:mall:list', JSON_OBJECT('title', 'Payment orders', 'icon', 'mdi:receipt-clock-outline', 'order', 4), 1)
ON DUPLICATE KEY UPDATE
  `pid` = VALUES(`pid`),
  `path` = VALUES(`path`),
  `component` = VALUES(`component`),
  `type` = VALUES(`type`),
  `auth_code` = VALUES(`auth_code`),
  `meta` = VALUES(`meta`),
  `status` = VALUES(`status`);

INSERT IGNORE INTO `bp_role_menu` (`role_id`, `menu_id`)
SELECT 1, id FROM `bp_menu` WHERE id BETWEEN 70 AND 89;

INSERT IGNORE INTO `bp_role_menu` (`role_id`, `menu_id`)
SELECT 2, id FROM `bp_menu` WHERE id IN (84, 85, 86, 87, 88, 89);

INSERT INTO `bp_package_global` (`id`, `name`, `price`, `valid_days`, `daily_limit`, `qps_limit`, `status`, `remark`)
VALUES (1, '基础全站套餐', 0.0000, 30, 100, 1, 1, '默认演示套餐')
ON DUPLICATE KEY UPDATE `name` = VALUES(`name`);

INSERT INTO `bp_package_point` (`id`, `name`, `price`, `points`, `status`, `remark`)
VALUES (1, '基础点数包', 0.0000, 100, 1, '默认演示点数包')
ON DUPLICATE KEY UPDATE `name` = VALUES(`name`);
