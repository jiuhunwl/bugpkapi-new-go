DELETE FROM `bp_role_menu` WHERE `menu_id` BETWEEN 170 AND 173;
DELETE FROM `bp_menu` WHERE `id` BETWEEN 170 AND 173;

ALTER TABLE `bp_interface_call_log`
  DROP KEY `idx_bp_interface_call_log_client_ip_time`,
  DROP KEY `idx_bp_interface_call_log_created_id`;
