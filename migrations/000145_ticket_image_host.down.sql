DELETE FROM `bp_role_menu` WHERE `menu_id` BETWEEN 900120 AND 900122;
DELETE FROM `bp_menu` WHERE `id` BETWEEN 900120 AND 900122;
DROP TABLE IF EXISTS `bp_image_host_config`;
ALTER TABLE `bp_ticket_attachment`
  DROP COLUMN `external_url`,
  DROP COLUMN `storage_provider`;
