ALTER TABLE `bp_role`
  ADD COLUMN `qps_limit` int NOT NULL DEFAULT 1 COMMENT 'Role baseline QPS' AFTER `remark`,
  ADD COLUMN `self_register_enabled` tinyint NOT NULL DEFAULT 0 COMMENT 'Whether public registration may select this role' AFTER `qps_limit`;

ALTER TABLE `bp_auth_register_config`
  ADD COLUMN `default_role_id` bigint DEFAULT NULL COMMENT 'Default role assigned by public registration' AFTER `register_enabled`;

ALTER TABLE `bp_user`
  ADD COLUMN `primary_role_id` bigint DEFAULT NULL COMMENT 'Primary role used for account QPS' AFTER `id`,
  ADD KEY `idx_bp_user_primary_role` (`primary_role_id`);

INSERT INTO `bp_role` (`role_key`, `role_name`, `remark`, `qps_limit`, `self_register_enabled`, `status`)
VALUES
  ('admin', '系统管理员', '系统内置管理员角色', 1, 0, 1),
  ('user', '普通用户', '系统内置普通用户角色', 1, 1, 1)
ON DUPLICATE KEY UPDATE
  `status` = IF(`role_key` = 'admin', 1, `status`);

UPDATE `bp_role`
SET `qps_limit` = LEAST(1000000, GREATEST(1, COALESCE((
  SELECT `default_user_qps`
  FROM `bp_auth_register_config`
  WHERE `id` = 1
), 1)));

UPDATE `bp_role`
SET `self_register_enabled` = CASE
  WHEN `role_key` = 'user' THEN 1
  ELSE 0
END
WHERE `role_key` IN ('admin', 'user');

UPDATE `bp_auth_register_config` cfg
JOIN `bp_role` role_user ON role_user.`role_key` = 'user'
SET cfg.`default_role_id` = role_user.`id`,
    cfg.`default_user_qps` = role_user.`qps_limit`
WHERE cfg.`id` = 1;

INSERT IGNORE INTO `bp_user_role` (`user_id`, `role_id`)
SELECT u.`id`, role_user.`id`
FROM `bp_user` u
JOIN `bp_role` role_user ON role_user.`role_key` = 'user' AND role_user.`status` = 1
LEFT JOIN `bp_user_role` ur ON ur.`user_id` = u.`id`
WHERE ur.`user_id` IS NULL;

UPDATE `bp_user` u
JOIN (
  SELECT
    ur.`user_id`,
    COALESCE(
      MAX(CASE WHEN r.`role_key` = 'admin' AND r.`status` = 1 THEN r.`id` END),
      MAX(CASE WHEN r.`role_key` = 'user' AND r.`status` = 1 THEN r.`id` END),
      MIN(CASE WHEN r.`status` = 1 THEN r.`id` END)
    ) AS `primary_role_id`
  FROM `bp_user_role` ur
  JOIN `bp_role` r ON r.`id` = ur.`role_id`
  GROUP BY ur.`user_id`
) selected_role ON selected_role.`user_id` = u.`id`
SET u.`primary_role_id` = selected_role.`primary_role_id`;
