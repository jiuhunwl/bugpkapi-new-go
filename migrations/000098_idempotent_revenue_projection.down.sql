-- Downgrades require all application instances stopped. Restore the legacy
-- read model from the authoritative ledger before removing new bookkeeping.
INSERT INTO bp_billing_revenue_total(id,confirmed_charge_amount)
SELECT 1,COALESCE(SUM(charge_amount),0) FROM bp_billing_record WHERE status='CONFIRMED'
ON DUPLICATE KEY UPDATE confirmed_charge_amount=VALUES(confirmed_charge_amount);
DROP TABLE IF EXISTS bp_billing_revenue_projection;
ALTER TABLE bp_billing_record
  DROP INDEX idx_bp_billing_revenue_pending,
  DROP COLUMN revenue_projected;
