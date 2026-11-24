-- A single-row revenue snapshot keeps dashboard reads independent of the
-- append-only billing-record cardinality. The analytics worker advances this
-- value in the same transaction as its source checkpoint.
CREATE TABLE IF NOT EXISTS `bp_billing_revenue_total` (
  `id` tinyint unsigned NOT NULL,
  `confirmed_charge_amount` decimal(38,4) NOT NULL DEFAULT 0.0000,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='Constant-cardinality confirmed revenue snapshot for dashboards';

INSERT INTO `bp_billing_revenue_total` (`id`, `confirmed_charge_amount`)
SELECT 1, COALESCE(SUM(`charge_amount`), 0)
FROM `bp_billing_record`
WHERE `status` = 'CONFIRMED'
ON DUPLICATE KEY UPDATE
  `confirmed_charge_amount` = VALUES(`confirmed_charge_amount`);
