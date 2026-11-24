DELETE FROM `bp_role_menu` WHERE `role_id`=1 AND `menu_id`=187;
DELETE FROM `bp_menu` WHERE `id`=187;
ALTER TABLE `bp_interface_api`
  DROP FOREIGN KEY `fk_bp_interface_response_ad`,
  DROP INDEX `idx_bp_interface_response_ad`,
  DROP INDEX `idx_bp_interface_cache_enabled`,
  DROP COLUMN `response_ad_field`,
  DROP COLUMN `response_ad_id`,
  DROP COLUMN `response_ad_enabled`,
  DROP COLUMN `cache_rules`,
  DROP COLUMN `cache_default_ttl_seconds`,
  DROP COLUMN `cache_enabled`;
