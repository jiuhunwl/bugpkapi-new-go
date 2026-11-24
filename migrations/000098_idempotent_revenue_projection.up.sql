-- Income projection is separate from the legacy additive snapshot so older
-- binaries cannot contaminate the new total during an upgrade. Existing and
-- future confirmed rows become durable work via a default-zero marker.
ALTER TABLE bp_billing_record
  ADD COLUMN revenue_projected tinyint unsigned NOT NULL DEFAULT 0,
  ADD KEY idx_bp_billing_revenue_pending (status, revenue_projected, id, charge_amount);

CREATE TABLE IF NOT EXISTS bp_billing_revenue_projection (
  id tinyint unsigned NOT NULL,
  confirmed_charge_amount decimal(38,4) NOT NULL DEFAULT 0.0000,
  updated_at datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

INSERT INTO bp_billing_revenue_projection(id,confirmed_charge_amount)
VALUES(1,0.0000)
ON DUPLICATE KEY UPDATE id=VALUES(id);
