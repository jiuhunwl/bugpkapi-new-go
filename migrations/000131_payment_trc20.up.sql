-- 000131: TRC20/USDT 链上充值通道（海外站）。
-- 充值订单额外记录链上收款地址、精确金额（含对账分位）、txid 与确认数；
-- 转账流水表以 txid 唯一幂等，轮询与用户提交共用同一份匹配依据。

CREATE TABLE IF NOT EXISTS `bp_payment_trc20_config` (
  `id` bigint NOT NULL DEFAULT 1 COMMENT 'singleton id',
  `enabled` tinyint NOT NULL DEFAULT 0 COMMENT 'TRC20 充值开关',
  `network` varchar(16) NOT NULL DEFAULT 'TRON' COMMENT '链网络',
  `address` varchar(64) NOT NULL DEFAULT '' COMMENT 'USDT-TRC20 收款地址',
  `api_key_cipher` text COMMENT 'TronGrid API Key（加密存储，可空）',
  `contract_address` varchar(64) NOT NULL DEFAULT 'TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t' COMMENT 'USDT 合约地址',
  `min_amount` decimal(18,6) NOT NULL DEFAULT 5.000000 COMMENT '最小充值 USDT',
  `max_amount` decimal(18,6) NOT NULL DEFAULT 10000.000000 COMMENT '单笔最大 USDT',
  `confirmations` int NOT NULL DEFAULT 1 COMMENT '入账所需确认数',
  `cursor_timestamp_ms` bigint NOT NULL DEFAULT 0 COMMENT '轮询游标（区块时间毫秒）',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  CONSTRAINT `chk_bp_trc20_network` CHECK (`network` IN ('TRON'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='TRC20 充值通道配置';

INSERT INTO `bp_payment_trc20_config` (`id`, `enabled`, `network`, `address`)
VALUES (1, 0, 'TRON', '')
ON DUPLICATE KEY UPDATE `id` = VALUES(`id`);

CREATE TABLE IF NOT EXISTS `bp_payment_trc20_transfer` (
  `id` bigint NOT NULL AUTO_INCREMENT,
  `txid` varchar(128) NOT NULL COMMENT '链上交易号',
  `from_address` varchar(64) NOT NULL DEFAULT '',
  `to_address` varchar(64) NOT NULL DEFAULT '',
  `amount` decimal(18,6) NOT NULL DEFAULT 0 COMMENT 'USDT 金额',
  `block_timestamp` bigint NOT NULL DEFAULT 0 COMMENT '区块时间（毫秒）',
  `matched_order_no` varchar(64) DEFAULT NULL COMMENT '匹配到的充值订单号',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_bp_trc20_transfer_txid` (`txid`),
  KEY `idx_bp_trc20_transfer_matched` (`matched_order_no`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='TRC20 到账流水（txid 幂等）';

ALTER TABLE `bp_payment_order`
  ADD COLUMN `chain_address` varchar(64) DEFAULT NULL COMMENT 'TRC20 收款地址快照' AFTER `channel_trade_no`,
  ADD COLUMN `chain_amount` decimal(18,6) DEFAULT NULL COMMENT 'TRC20 精确到账金额（USDT）' AFTER `chain_address`,
  ADD COLUMN `chain_txid` varchar(128) DEFAULT NULL COMMENT 'TRC20 交易号' AFTER `chain_amount`,
  ADD COLUMN `chain_confirm_count` int NOT NULL DEFAULT 0 COMMENT 'TRC20 确认数' AFTER `chain_txid`,
  ADD UNIQUE KEY `uk_bp_payment_order_chain_txid` (`chain_txid`),
  ADD KEY `idx_bp_payment_order_chain_match` (`pay_channel`, `status`, `chain_amount`);

ALTER TABLE `bp_payment_method` DROP CHECK `chk_bp_payment_method_code`;
ALTER TABLE `bp_payment_method`
  ADD CONSTRAINT `chk_bp_payment_method_code` CHECK (`method_code` IN ('BALANCE','ALIPAY','WECHAT','HUPIJIAO_ALIPAY','HUPIJIAO_WECHAT','TRC20'));

INSERT INTO `bp_payment_method` (`method_code`, `enabled`, `sort_order`)
VALUES ('TRC20', 0, 60)
ON DUPLICATE KEY UPDATE `method_code` = VALUES(`method_code`);
