-- 000135: 套餐商城「可见用户组」与「购买用户组」分离。
--
-- 背景：bp_package_role 同时承担两件事——商城可见性和购买资格。管理端一旦为了
-- 让 VIP 等用户组看到某个套餐而追加可见组，这些组也就同时获得了购买资格。
-- 新表只影响商城可见性（role_visible_only=1 时生效）；为空时回退旧的
-- bp_package_role 行为，历史套餐不需要迁移数据。
CREATE TABLE IF NOT EXISTS `bp_package_visible_role` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `package_type` varchar(16) NOT NULL COMMENT 'GLOBAL / INTERFACE / POINT',
  `package_id` bigint NOT NULL,
  `role_id` bigint NOT NULL,
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_package_visible_role` (`package_type`,`package_id`,`role_id`),
  KEY `idx_visible_role` (`role_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='套餐商城可见用户组（与购买权限分离；为空时回退购买组）';
