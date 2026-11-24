CREATE TABLE IF NOT EXISTS `bp_payment_alipay_config` (
  `id` bigint NOT NULL DEFAULT 1 COMMENT 'singleton id',
  `mode` varchar(16) NOT NULL DEFAULT 'SANDBOX' COMMENT 'SANDBOX/PRODUCTION',
  `app_id` varchar(64) NOT NULL DEFAULT '',
  `gateway_url` varchar(255) NOT NULL DEFAULT '',
  `app_private_key_cipher` text,
  `alipay_public_key` text,
  `seller_id` varchar(64) DEFAULT NULL,
  `notify_url` varchar(512) DEFAULT NULL,
  `return_url` varchar(512) DEFAULT NULL,
  `enabled` tinyint NOT NULL DEFAULT 0,
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='Alipay payment config';

INSERT INTO `bp_payment_alipay_config` (`id`, `mode`, `gateway_url`, `enabled`)
VALUES (1, 'SANDBOX', 'https://openapi-sandbox.dl.alipaydev.com/gateway.do', 0)
ON DUPLICATE KEY UPDATE `id` = `id`;

CREATE TABLE IF NOT EXISTS `bp_recharge_preset` (
  `id` bigint NOT NULL AUTO_INCREMENT,
  `name` varchar(128) NOT NULL,
  `amount` decimal(12,4) NOT NULL DEFAULT 0.0000,
  `gift_amount` decimal(12,4) NOT NULL DEFAULT 0.0000,
  `sort_order` int NOT NULL DEFAULT 0,
  `status` tinyint NOT NULL DEFAULT 1,
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  KEY `idx_bp_recharge_preset_status_sort` (`status`, `sort_order`, `id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='Recharge preset';

CREATE TABLE IF NOT EXISTS `bp_payment_order` (
  `id` bigint NOT NULL AUTO_INCREMENT,
  `order_no` varchar(64) NOT NULL,
  `user_id` bigint NOT NULL,
  `order_type` varchar(32) NOT NULL COMMENT 'RECHARGE/GLOBAL_PACKAGE/INTERFACE_PACKAGE/POINT_PACKAGE',
  `product` varchar(16) NOT NULL COMMENT 'PAGE/WAP/FACE',
  `subject` varchar(128) NOT NULL,
  `target_id` bigint DEFAULT NULL,
  `pay_amount` decimal(12,4) NOT NULL DEFAULT 0.0000,
  `gift_amount` decimal(12,4) NOT NULL DEFAULT 0.0000,
  `grant_points` bigint NOT NULL DEFAULT 0,
  `status` varchar(32) NOT NULL DEFAULT 'CREATED',
  `alipay_trade_no` varchar(128) DEFAULT NULL,
  `buyer_id` varchar(64) DEFAULT NULL,
  `pay_form` mediumtext,
  `pay_url` varchar(1024) DEFAULT NULL,
  `qr_code` varchar(1024) DEFAULT NULL,
  `expire_at` datetime NOT NULL,
  `paid_at` datetime DEFAULT NULL,
  `settled_at` datetime DEFAULT NULL,
  `snapshot` json DEFAULT NULL,
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_bp_payment_order_no` (`order_no`),
  KEY `idx_bp_payment_order_user` (`user_id`, `created_at`),
  KEY `idx_bp_payment_order_status` (`status`, `created_at`),
  KEY `idx_bp_payment_order_trade` (`alipay_trade_no`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='Payment order';

CREATE TABLE IF NOT EXISTS `bp_payment_notify_log` (
  `id` bigint NOT NULL AUTO_INCREMENT,
  `order_no` varchar(64) DEFAULT NULL,
  `trade_no` varchar(128) DEFAULT NULL,
  `notify_id` varchar(128) DEFAULT NULL,
  `event_type` varchar(32) NOT NULL DEFAULT 'ALIPAY_NOTIFY',
  `verified` tinyint NOT NULL DEFAULT 0,
  `processed` tinyint NOT NULL DEFAULT 0,
  `message` varchar(255) DEFAULT NULL,
  `payload` json DEFAULT NULL,
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  KEY `idx_bp_payment_notify_order` (`order_no`, `created_at`),
  KEY `idx_bp_payment_notify_trade` (`trade_no`, `created_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='Payment notify log';

INSERT INTO `bp_menu`
  (`id`, `pid`, `name`, `path`, `component`, `redirect`, `type`, `auth_code`, `meta`, `status`)
VALUES
  (100, 0, 'PaymentManagement', '/payment', NULL, NULL, 'catalog', 'payment:root', JSON_OBJECT('title', 'Payment', 'icon', 'lucide:credit-card', 'order', 9990), 1),
  (101, 100, 'PaymentConfig', '/payment/config', '/payment/config/index', NULL, 'menu', 'payment:config:view', JSON_OBJECT('title', 'Alipay Config', 'order', 1), 1),
  (102, 101, 'PaymentConfigUpdate', '', NULL, NULL, 'button', 'payment:config:update', JSON_OBJECT('title', 'Update Alipay Config'), 1),
  (103, 100, 'RechargePresetList', '/payment/amounts', '/payment/amount/index', NULL, 'menu', 'payment:preset:list', JSON_OBJECT('title', 'Recharge Presets', 'order', 2), 1),
  (104, 103, 'RechargePresetCreate', '', NULL, NULL, 'button', 'payment:preset:create', JSON_OBJECT('title', 'Create Recharge Preset'), 1),
  (105, 103, 'RechargePresetUpdate', '', NULL, NULL, 'button', 'payment:preset:update', JSON_OBJECT('title', 'Update Recharge Preset'), 1),
  (106, 103, 'RechargePresetDelete', '', NULL, NULL, 'button', 'payment:preset:delete', JSON_OBJECT('title', 'Delete Recharge Preset'), 1),
  (107, 100, 'PaymentOrderList', '/payment/orders', '/payment/order/index', NULL, 'menu', 'payment:order:list', JSON_OBJECT('title', 'Payment Orders', 'order', 3), 1),
  (108, 107, 'PaymentOrderDetail', '', NULL, NULL, 'button', 'payment:order:detail', JSON_OBJECT('title', 'Payment Order Detail'), 1),
  (109, 107, 'PaymentOrderSync', '', NULL, NULL, 'button', 'payment:order:sync', JSON_OBJECT('title', 'Sync Payment Order'), 1)
ON DUPLICATE KEY UPDATE
  `pid` = VALUES(`pid`),
  `path` = VALUES(`path`),
  `component` = VALUES(`component`),
  `type` = VALUES(`type`),
  `auth_code` = VALUES(`auth_code`),
  `meta` = VALUES(`meta`),
  `status` = VALUES(`status`);

INSERT IGNORE INTO `bp_role_menu` (`role_id`, `menu_id`)
SELECT 1, id FROM `bp_menu` WHERE id BETWEEN 100 AND 109;

INSERT INTO `bp_recharge_preset` (`id`, `name`, `amount`, `gift_amount`, `sort_order`, `status`)
VALUES
  (1, 'Recharge 50', 50.0000, 0.0000, 1, 1),
  (2, 'Recharge 100', 100.0000, 5.0000, 2, 1)
ON DUPLICATE KEY UPDATE `name` = VALUES(`name`);
