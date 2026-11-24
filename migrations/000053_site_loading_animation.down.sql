DELETE FROM `bp_role_menu` WHERE `menu_id`=189;
DELETE FROM `bp_menu` WHERE `id`=189;

ALTER TABLE `bp_site_config`
  DROP COLUMN `loading_animation_url`,
  DROP COLUMN `loading_animation_enabled`;
