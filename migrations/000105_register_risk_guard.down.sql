DROP TABLE IF EXISTS `bp_register_ip_used`;
DROP TABLE IF EXISTS `bp_email_domain_rule`;
ALTER TABLE `bp_auth_register_config`
  DROP COLUMN `register_ip_max_accounts`;
