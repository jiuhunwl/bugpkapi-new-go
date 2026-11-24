ALTER TABLE `bp_user`
  DROP INDEX `idx_bp_user_primary_role`,
  DROP COLUMN `primary_role_id`;

ALTER TABLE `bp_auth_register_config`
  DROP COLUMN `default_role_id`;

ALTER TABLE `bp_role`
  DROP COLUMN `self_register_enabled`,
  DROP COLUMN `qps_limit`;
