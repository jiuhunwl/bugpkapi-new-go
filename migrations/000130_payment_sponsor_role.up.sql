-- 充值赞助自动升级用户组（2026-11-04）。
-- RECHARGE 订单结算入账时，把付款用户的主用户组切换到本表配置的目标用户组。
-- enabled=0（默认）时保持原行为；role_id=0 视为未配置。
CREATE TABLE IF NOT EXISTS `bp_payment_sponsor_config` (
  `id` bigint NOT NULL COMMENT '单例行，恒为 1',
  `enabled` tinyint NOT NULL DEFAULT 0 COMMENT '充值后自动切换用户组：0关闭，1开启',
  `role_id` bigint NOT NULL DEFAULT 0 COMMENT '目标用户组编号，0=未配置',
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='充值赞助自动升级用户组配置';

INSERT INTO `bp_payment_sponsor_config` (`id`, `enabled`, `role_id`)
VALUES (1, 0, 0)
ON DUPLICATE KEY UPDATE `id` = VALUES(`id`);
