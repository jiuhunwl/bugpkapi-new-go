-- 回滚前必须先把 ignore 行改回 inherit，否则旧 CHECK 约束无法建立。
UPDATE `bp_interface_role_quota` SET `mode` = 'inherit' WHERE `mode` = 'ignore';

ALTER TABLE `bp_interface_role_quota` DROP CHECK `chk_bp_interface_role_quota_mode`;
ALTER TABLE `bp_interface_role_quota`
  ADD CONSTRAINT `chk_bp_interface_role_quota_mode`
  CHECK (`mode` IN ('inherit', 'unlimited', 'adjust', 'override'));
