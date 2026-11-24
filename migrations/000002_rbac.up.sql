ALTER TABLE `bp_role`
  ADD COLUMN `remark` varchar(255) DEFAULT NULL COMMENT '角色备注' AFTER `role_name`;

CREATE TABLE IF NOT EXISTS `bp_menu` (
  `id` bigint NOT NULL AUTO_INCREMENT COMMENT '菜单编号',
  `pid` bigint NOT NULL DEFAULT 0 COMMENT '父级菜单编号',
  `name` varchar(128) NOT NULL COMMENT '路由名称',
  `path` varchar(255) NOT NULL DEFAULT '' COMMENT '路由路径',
  `component` varchar(255) DEFAULT NULL COMMENT '前端组件路径',
  `redirect` varchar(255) DEFAULT NULL COMMENT '重定向路径',
  `type` varchar(32) NOT NULL COMMENT '类型：catalog/menu/embedded/link/button',
  `auth_code` varchar(128) DEFAULT NULL COMMENT '权限标识',
  `meta` json DEFAULT NULL COMMENT '前端路由meta',
  `status` tinyint NOT NULL DEFAULT 1 COMMENT '状态：1启用，0禁用',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_bp_menu_name` (`name`),
  KEY `idx_bp_menu_pid` (`pid`),
  KEY `idx_bp_menu_path` (`path`),
  KEY `idx_bp_menu_auth_code` (`auth_code`),
  KEY `idx_bp_menu_status` (`status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='RBAC菜单表';

CREATE TABLE IF NOT EXISTS `bp_role_menu` (
  `role_id` bigint NOT NULL COMMENT '角色编号',
  `menu_id` bigint NOT NULL COMMENT '菜单编号',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  PRIMARY KEY (`role_id`, `menu_id`),
  KEY `idx_bp_role_menu_menu` (`menu_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='RBAC角色菜单关联表';

INSERT INTO `bp_menu`
  (`id`, `pid`, `name`, `path`, `component`, `redirect`, `type`, `auth_code`, `meta`, `status`)
VALUES
  (1, 0, 'Workspace', '/console/workspace', '/dashboard/workspace/index', NULL, 'menu', 'workspace:view', JSON_OBJECT('title', 'page.dashboard.workspace', 'icon', 'carbon:workspace', 'order', -1, 'affixTab', true, 'keepAlive', true), 1),
  (10, 0, 'UserManagement', '/user', NULL, NULL, 'catalog', 'system:user:root', JSON_OBJECT('title', 'system.user.title', 'icon', 'mdi:account-cog', 'order', 9996), 1),
  (11, 10, 'SystemUser', '/user/list', '/system/user/list', NULL, 'menu', 'system:user:list', JSON_OBJECT('title', 'system.user.listTitle', 'icon', 'mdi:account', 'order', 1), 1),
  (12, 11, 'SystemUserCreate', '', NULL, NULL, 'button', 'system:user:create', JSON_OBJECT('title', '新增用户', 'order', 1), 1),
  (13, 11, 'SystemUserUpdate', '', NULL, NULL, 'button', 'system:user:update', JSON_OBJECT('title', '编辑用户', 'order', 2), 1),
  (14, 11, 'SystemUserDelete', '', NULL, NULL, 'button', 'system:user:delete', JSON_OBJECT('title', '删除用户', 'order', 3), 1),
  (15, 11, 'SystemUserSpecifiedResponse', '', NULL, NULL, 'button', 'system:user:specified-response', JSON_OBJECT('title', '指定返回配置', 'order', 4), 1),
  (20, 10, 'SystemRole', '/user/role', '/system/role/list', NULL, 'menu', 'system:role:list', JSON_OBJECT('title', 'system.role.title', 'icon', 'mdi:account-group', 'order', 2), 1),
  (21, 20, 'SystemRoleCreate', '', NULL, NULL, 'button', 'system:role:create', JSON_OBJECT('title', '新增角色', 'order', 1), 1),
  (22, 20, 'SystemRoleUpdate', '', NULL, NULL, 'button', 'system:role:update', JSON_OBJECT('title', '编辑角色', 'order', 2), 1),
  (23, 20, 'SystemRoleDelete', '', NULL, NULL, 'button', 'system:role:delete', JSON_OBJECT('title', '删除角色', 'order', 3), 1),
  (30, 0, 'System', '/system', NULL, NULL, 'catalog', 'system:menu:root', JSON_OBJECT('title', 'system.title', 'icon', 'ion:settings-outline', 'order', 9997), 1),
  (31, 30, 'SystemMenu', '/system/menu', '/system/menu/list', NULL, 'menu', 'system:menu:list', JSON_OBJECT('title', 'system.menu.title', 'icon', 'mdi:menu', 'order', 1), 1),
  (32, 31, 'SystemMenuCreate', '', NULL, NULL, 'button', 'system:menu:create', JSON_OBJECT('title', '新增菜单', 'order', 1), 1),
  (33, 31, 'SystemMenuUpdate', '', NULL, NULL, 'button', 'system:menu:update', JSON_OBJECT('title', '编辑菜单', 'order', 2), 1),
  (34, 31, 'SystemMenuDelete', '', NULL, NULL, 'button', 'system:menu:delete', JSON_OBJECT('title', '删除菜单', 'order', 3), 1)
ON DUPLICATE KEY UPDATE
  `pid` = VALUES(`pid`),
  `path` = VALUES(`path`),
  `component` = VALUES(`component`),
  `redirect` = VALUES(`redirect`),
  `type` = VALUES(`type`),
  `auth_code` = VALUES(`auth_code`),
  `meta` = VALUES(`meta`),
  `status` = VALUES(`status`);

INSERT IGNORE INTO `bp_role_menu` (`role_id`, `menu_id`)
SELECT 1, id FROM `bp_menu`;

INSERT IGNORE INTO `bp_role_menu` (`role_id`, `menu_id`) VALUES (2, 1);
