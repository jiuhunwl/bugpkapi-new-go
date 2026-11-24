CREATE TABLE IF NOT EXISTS `bp_payment_wechat_config` (
  `id` bigint NOT NULL DEFAULT 1 COMMENT 'singleton id',
  `enabled` tinyint NOT NULL DEFAULT 0,
  `mode` varchar(16) NOT NULL DEFAULT 'SANDBOX' COMMENT 'SANDBOX/PRODUCTION',
  `mchid` varchar(64) NOT NULL DEFAULT '',
  `app_id` varchar(64) NOT NULL DEFAULT '',
  `apiv3_key_cipher` text,
  `private_key_cipher` text,
  `serial_no` varchar(64) DEFAULT NULL COMMENT '商户API证书序列号',
  `platform_public_key_id` varchar(64) DEFAULT NULL,
  `platform_public_key_cipher` text,
  `notify_url` varchar(512) DEFAULT NULL,
  `sp_mchid` varchar(64) DEFAULT NULL,
  `sp_app_id` varchar(64) DEFAULT NULL,
  `sub_mchid` varchar(64) DEFAULT NULL,
  `sub_app_id` varchar(64) DEFAULT NULL,
  `oa_app_id` varchar(64) DEFAULT NULL COMMENT '公众号 appid(JSAPI)',
  `mini_app_id` varchar(64) DEFAULT NULL COMMENT '小程序 appid',
  `app_app_id` varchar(64) DEFAULT NULL COMMENT 'APP appid',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='WeChat Pay v3 config';

INSERT INTO `bp_payment_wechat_config` (`id`, `mode`, `enabled`)
VALUES (1, 'SANDBOX', 0)
ON DUPLICATE KEY UPDATE `id` = `id`;

ALTER TABLE `bp_payment_order`
  ADD COLUMN `trade_type` varchar(16) DEFAULT NULL COMMENT 'WECHAT: NATIVE/H5/JSAPI/APP/MINI; 支付宝留空' AFTER `product`,
  ADD COLUMN `prepay_id` varchar(64) DEFAULT NULL COMMENT '微信预支付ID' AFTER `qr_code`,
  ADD COLUMN `channel_trade_no` varchar(128) DEFAULT NULL COMMENT '通道交易号(微信transaction_id)' AFTER `alipay_trade_no`;

-- 微信配置权限按钮（复用支付配置菜单 101 作为父级，auth_code 独立便于灰度）。
INSERT INTO `bp_menu`
  (`id`, `pid`, `name`, `path`, `component`, `redirect`, `type`, `auth_code`, `meta`, `status`)
VALUES
  (111, 101, 'WechatConfigUpdate', '', NULL, NULL, 'button', 'payment:wechat:update', JSON_OBJECT('title', 'Update WeChat Config'), 1)
ON DUPLICATE KEY UPDATE `pid` = VALUES(`pid`), `auth_code` = VALUES(`auth_code`), `meta` = VALUES(`meta`), `status` = VALUES(`status`);

INSERT IGNORE INTO `bp_role_menu` (`role_id`, `menu_id`) SELECT 1, 111;
