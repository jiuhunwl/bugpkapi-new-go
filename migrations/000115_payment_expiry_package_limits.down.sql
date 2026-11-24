ALTER TABLE `bp_payment_order`
  DROP KEY `idx_bp_payment_order_expire`,
  DROP KEY `idx_bp_payment_order_reuse`,
  DROP COLUMN `stock_reserved`;

ALTER TABLE `bp_package_interface_spec`
  DROP KEY `idx_bp_package_interface_spec_sale`,
  DROP COLUMN `sale_end_at`,
  DROP COLUMN `sale_start_at`,
  DROP COLUMN `per_user_limit`,
  DROP COLUMN `stock_used`,
  DROP COLUMN `stock_limit`;

ALTER TABLE `bp_package_point`
  DROP KEY `idx_bp_package_point_sale`,
  DROP COLUMN `sale_end_at`,
  DROP COLUMN `sale_start_at`,
  DROP COLUMN `per_user_limit`,
  DROP COLUMN `stock_used`,
  DROP COLUMN `stock_limit`;

ALTER TABLE `bp_package_global`
  DROP KEY `idx_bp_package_global_sale`,
  DROP COLUMN `sale_end_at`,
  DROP COLUMN `sale_start_at`,
  DROP COLUMN `per_user_limit`,
  DROP COLUMN `stock_used`,
  DROP COLUMN `stock_limit`;
