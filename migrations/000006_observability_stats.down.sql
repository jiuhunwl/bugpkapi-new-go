DELETE FROM `bp_role_menu` WHERE `menu_id` BETWEEN 90 AND 96;
DELETE FROM `bp_menu` WHERE `id` BETWEEN 90 AND 96;

DROP TABLE IF EXISTS `bp_user_log_cleanup_config`;

ALTER TABLE `bp_interface_call_log`
  DROP KEY `idx_bp_interface_call_log_billing_status`,
  DROP KEY `idx_bp_interface_call_log_trace`,
  DROP KEY `idx_bp_interface_call_log_region`,
  DROP COLUMN `trace_id`,
  DROP COLUMN `region_provider`,
  DROP COLUMN `longitude`,
  DROP COLUMN `latitude`,
  DROP COLUMN `ip_isp`,
  DROP COLUMN `ip_city`,
  DROP COLUMN `ip_province`,
  DROP COLUMN `ip_country`,
  DROP COLUMN `response_size`,
  DROP COLUMN `request_size`,
  DROP COLUMN `api_key_prefix`;
