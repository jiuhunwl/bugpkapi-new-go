CREATE TABLE IF NOT EXISTS `bp_payment_wechat_platform_cert` (
  `id` bigint NOT NULL AUTO_INCREMENT,
  `serial_no` varchar(64) NOT NULL,
  `public_key_cipher` text,
  `effective_time` datetime DEFAULT NULL,
  `expire_time` datetime DEFAULT NULL,
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_wechat_cert_serial` (`serial_no`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='WeChat platform cert cache';
