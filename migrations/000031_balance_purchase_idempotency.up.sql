CREATE TABLE IF NOT EXISTS `bp_balance_purchase_request` (
  `id` bigint NOT NULL AUTO_INCREMENT,
  `user_id` bigint NOT NULL,
  `idempotency_key` varchar(64) NOT NULL,
  `purchase_type` varchar(16) NOT NULL COMMENT 'GLOBAL/INTERFACE/POINT',
  `target_id` bigint NOT NULL,
  `status` varchar(16) NOT NULL DEFAULT 'PROCESSING' COMMENT 'PROCESSING/COMPLETED',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `completed_at` datetime DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_bp_balance_purchase_request_user_key` (`user_id`, `idempotency_key`),
  KEY `idx_bp_balance_purchase_request_created` (`created_at`, `id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='Balance purchase idempotency ledger';
