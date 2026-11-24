-- 000141: 币安 Pay 商户充值通道（覆盖「链下转账」）。
--
-- 为什么需要独立通道：交易所站内转账不产生任何链上交易，链上扫描永远看不到；
-- 只有币安自己的推送/查单能证明这笔钱存在。因此它与 TRC20 并列，不能替代。
--
-- nonce 表用于回调幂等：币安会重复推送同一笔，重复必须被识别为已处理。

CREATE TABLE IF NOT EXISTS `bp_payment_binancepay_config` (
  `id` bigint NOT NULL DEFAULT 1 COMMENT 'singleton id',
  `enabled` tinyint NOT NULL DEFAULT 0 COMMENT '币安 Pay 通道开关',
  `merchant_id` varchar(64) NOT NULL DEFAULT '' COMMENT 'Binance Pay 商户号',
  `cert_serial` varchar(128) NOT NULL DEFAULT '' COMMENT '商户 API 证书序列号',
  `secret_key_cipher` text COMMENT 'HMAC-SHA512 密钥（加密存储）',
  `api_key_cipher` text COMMENT 'Binance API Key（加密存储，可空）',
  `pay_currency` varchar(16) NOT NULL DEFAULT 'USDT' COMMENT '收款币种',
  `min_amount` decimal(18,6) NOT NULL DEFAULT 5.000000 COMMENT '最小充值金额',
  `max_amount` decimal(18,6) NOT NULL DEFAULT 10000.000000 COMMENT '单笔最大金额',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='币安 Pay 商户配置';

INSERT INTO `bp_payment_binancepay_config` (`id`, `enabled`, `pay_currency`)
VALUES (1, 0, 'USDT')
ON DUPLICATE KEY UPDATE `id` = VALUES(`id`);

-- 回调幂等键：同一 nonce 只允许入账一次。
-- 主键即业务键，重复插入直接失败，天然防重放。
CREATE TABLE IF NOT EXISTS `bp_payment_binancepay_nonce` (
  `nonce_key` varchar(160) NOT NULL COMMENT '商户号:nonce',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (`nonce_key`),
  KEY `idx_bp_binancepay_nonce_created` (`created_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='币安 Pay 回调幂等记录';

-- 币安 Pay 不产生链上交易号，但结算事务里 txid 那列被用来存渠道流水号。
-- 订单表的 chain_txid 长度足够容纳币安 transactionId，无需变更。
