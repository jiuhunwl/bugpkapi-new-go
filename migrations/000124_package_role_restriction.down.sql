DROP TABLE IF EXISTS `bp_package_role`;

ALTER TABLE `bp_package_interface`
  DROP COLUMN `role_visible_only`,
  DROP COLUMN `role_restricted`;

ALTER TABLE `bp_package_point`
  DROP COLUMN `role_visible_only`,
  DROP COLUMN `role_restricted`;

ALTER TABLE `bp_package_global`
  DROP COLUMN `role_visible_only`,
  DROP COLUMN `role_restricted`;
