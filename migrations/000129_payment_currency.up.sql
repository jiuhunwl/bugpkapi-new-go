-- 000129: 支付货币配置（人民币/美元，后台可切换）。
-- 站点结算货币为单选：CNY（默认，行为与历史一致）/ USD。
-- USD 模式下订单 pay_amount 按 usd_cny_rate 从商品原价（人民币）换算，
-- 原价保留在 bp_payment_order.origin_amount 便于审计与对账。

CREATE TABLE IF NOT EXISTS `bp_payment_currency_config` (
  `id` bigint NOT NULL DEFAULT 1 COMMENT 'singleton id',
  `currency` varchar(8) NOT NULL DEFAULT 'CNY' COMMENT 'settlement currency: CNY/USD',
  `usd_cny_rate` decimal(12,4) NOT NULL DEFAULT 7.2000 COMMENT '1 USD = X CNY, applied when currency=USD',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='Payment currency config';

INSERT INTO `bp_payment_currency_config` (`id`, `currency`, `usd_cny_rate`)
VALUES (1, 'CNY', 7.2000)
ON DUPLICATE KEY UPDATE `id` = `id`;

ALTER TABLE `bp_payment_order`
  ADD COLUMN `currency` varchar(8) NOT NULL DEFAULT 'CNY' COMMENT 'settlement currency at order time' AFTER `pay_channel`,
  ADD COLUMN `origin_amount` decimal(12,4) DEFAULT NULL COMMENT 'original CNY price before currency conversion' AFTER `currency`;
