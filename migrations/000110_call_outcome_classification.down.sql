ALTER TABLE `bp_call_api_metric_minute`
  DROP COLUMN `upstream_error_count`,
  DROP COLUMN `throttle_count`,
  DROP COLUMN `client_error_count`,
  DROP COLUMN `expected_empty_count`;

ALTER TABLE `bp_call_user_metric_minute`
  DROP COLUMN `upstream_error_count`,
  DROP COLUMN `throttle_count`,
  DROP COLUMN `client_error_count`,
  DROP COLUMN `expected_empty_count`;

ALTER TABLE `bp_call_metric_minute`
  DROP COLUMN `upstream_error_count`,
  DROP COLUMN `throttle_count`,
  DROP COLUMN `client_error_count`,
  DROP COLUMN `expected_empty_count`;

ALTER TABLE `bp_interface_call_log`
  DROP COLUMN `outcome_class`;
