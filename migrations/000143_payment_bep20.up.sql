-- 000143: BEP20（BSC）USDT 链上充值通道。
--
-- 与 TRC20 并列的第二种 USDT 收款网络：同一个充值订单模型（精确到分位金额用于
-- 链上匹配），但使用 EVM JSON-RPC（eth_getLogs / eth_getTransactionReceipt）对账，
-- 不依赖第三方索引 API Key。收款地址、RPC 节点、金额范围与确认数均可后台配置。

CREATE TABLE IF NOT EXISTS `bp_payment_bep20_config` (
  `id` bigint NOT NULL DEFAULT 1 COMMENT 'singleton id',
  `enabled` tinyint NOT NULL DEFAULT 0 COMMENT 'BEP20 充值开关',
  `network` varchar(16) NOT NULL DEFAULT 'BSC' COMMENT '链网络',
  `address` varchar(64) NOT NULL DEFAULT '' COMMENT 'USDT-BEP20 收款地址（0x 开头）',
  `contract_address` varchar(64) NOT NULL DEFAULT '0x55d398326f99059ff775485246999027b3197955' COMMENT 'BSC USDT 合约地址',
  `rpc_url` varchar(255) NOT NULL DEFAULT 'https://bsc-rpc.publicnode.com' COMMENT 'BSC JSON-RPC 节点',
  `min_amount` decimal(18,6) NOT NULL DEFAULT 5.000000 COMMENT '最小充值 USDT',
  `max_amount` decimal(18,6) NOT NULL DEFAULT 10000.000000 COMMENT '单笔最大 USDT',
  `confirmations` int NOT NULL DEFAULT 15 COMMENT '入账所需区块确认数',
  `cursor_block` bigint NOT NULL DEFAULT 0 COMMENT '已完成的扫描区块高水位',
  `retry_cursor_id` bigint NOT NULL DEFAULT 0 COMMENT '逐笔重试游标',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  CONSTRAINT `chk_bp_bep20_network` CHECK (`network` IN ('BSC'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='BEP20 充值通道配置';

INSERT INTO `bp_payment_bep20_config` (`id`, `enabled`, `network`, `address`)
VALUES (1, 0, 'BSC', '')
ON DUPLICATE KEY UPDATE `id` = VALUES(`id`);

-- 一笔 EVM 交易可能产生多个 Transfer 日志（合约归集、批量转账），
-- 因此幂等键是 (tx_hash, log_index)，而不是只看交易号。
CREATE TABLE IF NOT EXISTS `bp_payment_bep20_transfer` (
  `id` bigint NOT NULL AUTO_INCREMENT,
  `tx_hash` varchar(80) NOT NULL COMMENT '链上交易哈希（0x + 64 hex）',
  `log_index` int NOT NULL DEFAULT 0 COMMENT '交易内日志序号',
  `from_address` varchar(64) NOT NULL DEFAULT '',
  `to_address` varchar(64) NOT NULL DEFAULT '',
  `amount` decimal(18,6) NOT NULL DEFAULT 0 COMMENT 'USDT 金额',
  `block_number` bigint NOT NULL DEFAULT 0 COMMENT '区块号',
  `block_timestamp` bigint NOT NULL DEFAULT 0 COMMENT '区块时间（毫秒）',
  `matched_order_no` varchar(64) DEFAULT NULL COMMENT '匹配到的充值订单号',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_bp_bep20_transfer_log` (`tx_hash`, `log_index`),
  KEY `idx_bp_bep20_transfer_matched` (`matched_order_no`),
  KEY `idx_bp_bep20_transfer_to_block` (`to_address`, `block_number`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='BEP20 到账流水（tx+log 幂等）';

ALTER TABLE `bp_payment_method` DROP CHECK `chk_bp_payment_method_code`;
ALTER TABLE `bp_payment_method`
  ADD CONSTRAINT `chk_bp_payment_method_code` CHECK (`method_code` IN ('BALANCE','ALIPAY','WECHAT','HUPIJIAO_ALIPAY','HUPIJIAO_WECHAT','TRC20','BEP20'));

INSERT INTO `bp_payment_method` (`method_code`, `enabled`, `sort_order`)
VALUES ('BEP20', 0, 65)
ON DUPLICATE KEY UPDATE `method_code` = VALUES(`method_code`);
