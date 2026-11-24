CREATE TABLE IF NOT EXISTS `bp_payment_hupijiao_config` (
  `method` varchar(16) NOT NULL COMMENT 'ALIPAY/WECHAT',
  `enabled` tinyint NOT NULL DEFAULT 0,
  `app_id` varchar(128) NOT NULL DEFAULT '',
  `app_secret_cipher` text,
  `create_url` varchar(512) NOT NULL DEFAULT 'https://api.xunhupay.com/payment/do.html',
  `query_url` varchar(512) NOT NULL DEFAULT 'https://api.xunhupay.com/payment/query.html',
  `notify_url` varchar(512) DEFAULT NULL,
  `return_url` varchar(512) DEFAULT NULL,
  `callback_url` varchar(512) DEFAULT NULL,
  `wap_url` varchar(512) DEFAULT NULL,
  `wap_name` varchar(128) DEFAULT NULL,
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`method`),
  CONSTRAINT `chk_bp_hupijiao_method` CHECK (`method` IN ('ALIPAY','WECHAT'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='Hupijiao payment channel config';

INSERT INTO `bp_payment_hupijiao_config` (`method`, `enabled`)
VALUES ('ALIPAY', 0), ('WECHAT', 0)
ON DUPLICATE KEY UPDATE `method` = VALUES(`method`);

CREATE TABLE IF NOT EXISTS `bp_payment_method` (
  `method_code` varchar(32) NOT NULL,
  `enabled` tinyint NOT NULL DEFAULT 0,
  `sort_order` int NOT NULL DEFAULT 0,
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`method_code`),
  KEY `idx_bp_payment_method_enabled_sort` (`enabled`, `sort_order`, `method_code`),
  CONSTRAINT `chk_bp_payment_method_code` CHECK (`method_code` IN ('BALANCE','ALIPAY','WECHAT','HUPIJIAO_ALIPAY','HUPIJIAO_WECHAT'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='Checkout payment methods';

INSERT INTO `bp_payment_method` (`method_code`, `enabled`, `sort_order`)
VALUES
  ('BALANCE', 1, 10),
  ('ALIPAY', 1, 20),
  ('WECHAT', 0, 30),
  ('HUPIJIAO_ALIPAY', 0, 40),
  ('HUPIJIAO_WECHAT', 0, 50)
ON DUPLICATE KEY UPDATE `method_code` = VALUES(`method_code`);

SET @bugpk_payment_order_backfill_max_id = (SELECT COALESCE(MAX(`id`), 0) FROM `bp_payment_order`);

ALTER TABLE `bp_payment_order`
  ADD COLUMN `pay_channel` varchar(32) NOT NULL DEFAULT 'ALIPAY' COMMENT 'ALIPAY/WECHAT/HUPIJIAO_ALIPAY/HUPIJIAO_WECHAT' AFTER `product`,
  ADD KEY `idx_bp_payment_order_channel_status` (`pay_channel`, `status`, `created_at`);

UPDATE `bp_payment_order`
SET `pay_channel` = CASE
  WHEN `trade_type` IN ('NATIVE','H5','JSAPI','APP','MINI') THEN 'WECHAT'
  ELSE 'ALIPAY'
END
WHERE `id` <= @bugpk_payment_order_backfill_max_id
  AND `pay_channel` = 'ALIPAY';

INSERT INTO `bp_menu`
  (`id`, `pid`, `name`, `path`, `component`, `redirect`, `type`, `auth_code`, `meta`, `status`)
VALUES
  (112, 101, 'HupijiaoConfigUpdate', '', NULL, NULL, 'button', 'payment:hupijiao:update', JSON_OBJECT('title', 'Update Hupijiao Config'), 1),
  (113, 101, 'PaymentMethodUpdate', '', NULL, NULL, 'button', 'payment:method:update', JSON_OBJECT('title', 'Update Payment Methods'), 1)
ON DUPLICATE KEY UPDATE
  `pid` = VALUES(`pid`),
  `auth_code` = VALUES(`auth_code`),
  `meta` = VALUES(`meta`),
  `status` = VALUES(`status`);

INSERT IGNORE INTO `bp_role_menu` (`role_id`, `menu_id`)
SELECT r.`id`, m.`id`
FROM `bp_role` r
CROSS JOIN `bp_menu` m
WHERE r.`role_key` = 'admin'
  AND m.`id` IN (112, 113);
