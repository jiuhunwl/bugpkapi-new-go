-- BugPk-Api production initialization SQL
-- Generated from migrations 000001 through 000147.
-- User and user-role seeds are intentionally omitted; INITIAL_ADMIN_PASSWORD bootstraps the first administrator.
-- Select an EMPTY database with mysql --database=<name> before importing. No database is selected by this file.

SET NAMES utf8mb4;

-- Import in a NEW autocommit mysql CLI session, without --force; never apply initialization to an existing database.
SET @__bugpk_init_lock = CAST(MOD(CRC32(CONCAT(DATABASE(), ':schema_migrations')) * 1486364155, 4294967296) AS CHAR);
SET @__bugpk_init_acquired = GET_LOCK(@__bugpk_init_lock, 0);
SET @__bugpk_init_guard = IF(@__bugpk_init_acquired = 1 AND DATABASE() IS NOT NULL AND @@SESSION.autocommit = 1 AND NOT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema=DATABASE()), 'DO 0', 'SELECT * FROM __BUGPK_INIT_REQUIRES_EMPTY_SELECTED_DATABASE__');
PREPARE bugpk_init_guard_stmt FROM @__bugpk_init_guard;
EXECUTE bugpk_init_guard_stmt;
DEALLOCATE PREPARE bugpk_init_guard_stmt;
SET FOREIGN_KEY_CHECKS = 0;


-- ============================================================
-- Migration: 000001_init.up.sql
-- ============================================================

CREATE TABLE IF NOT EXISTS `bp_user` (
  `id` bigint NOT NULL AUTO_INCREMENT COMMENT '用户编号',
  `username` varchar(64) NOT NULL COMMENT '登录账号',
  `password_hash` varchar(100) NOT NULL COMMENT 'bcrypt密码哈希',
  `display_name` varchar(64) NOT NULL COMMENT '显示名称',
  `avatar` varchar(512) DEFAULT NULL COMMENT '头像地址',
  `email` varchar(128) DEFAULT NULL COMMENT '邮箱',
  `email_verified` tinyint NOT NULL DEFAULT 0 COMMENT '邮箱是否已验证',
  `mobile` varchar(32) DEFAULT NULL COMMENT '手机号',
  `mobile_verified` tinyint NOT NULL DEFAULT 0 COMMENT '手机号是否已验证',
  `home_path` varchar(128) NOT NULL DEFAULT '/console/workspace' COMMENT '登录后首页路径',
  `balance` decimal(12,4) NOT NULL DEFAULT 0.0000 COMMENT '账户余额',
  `points` bigint NOT NULL DEFAULT 0 COMMENT '账户点数',
  `status` tinyint NOT NULL DEFAULT 1 COMMENT '状态：1启用，0禁用',
  `token_version` int NOT NULL DEFAULT 0 COMMENT 'Token版本号，密码变更后递增',
  `last_login_at` datetime DEFAULT NULL COMMENT '最后登录时间',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_bp_user_username` (`username`),
  UNIQUE KEY `uk_bp_user_email` (`email`),
  UNIQUE KEY `uk_bp_user_mobile` (`mobile`),
  KEY `idx_bp_user_status` (`status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='BugPk用户表';

CREATE TABLE IF NOT EXISTS `bp_role` (
  `id` bigint NOT NULL AUTO_INCREMENT COMMENT '角色编号',
  `role_key` varchar(64) NOT NULL COMMENT '角色标识',
  `role_name` varchar(64) NOT NULL COMMENT '角色名称',
  `status` tinyint NOT NULL DEFAULT 1 COMMENT '状态：1启用，0禁用',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_bp_role_key` (`role_key`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='BugPk角色表';

CREATE TABLE IF NOT EXISTS `bp_user_role` (
  `user_id` bigint NOT NULL COMMENT '用户编号',
  `role_id` bigint NOT NULL COMMENT '角色编号',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  PRIMARY KEY (`user_id`, `role_id`),
  KEY `idx_bp_user_role_role` (`role_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='BugPk用户角色关联表';

CREATE TABLE IF NOT EXISTS `bp_auth_register_config` (
  `id` tinyint NOT NULL COMMENT '配置编号，固定为1',
  `register_enabled` tinyint NOT NULL DEFAULT 1 COMMENT '是否开放注册',
  `default_user_qps` int NOT NULL DEFAULT 1 COMMENT '普通用户默认QPS',
  `register_gift_points` bigint NOT NULL DEFAULT 0 COMMENT '注册赠送点数',
  `email_register_enabled` tinyint NOT NULL DEFAULT 0 COMMENT '是否开启邮箱注册',
  `mobile_register_enabled` tinyint NOT NULL DEFAULT 0 COMMENT '是否开启手机注册',
  `verification_code_ip_minute_limit` int NOT NULL DEFAULT 5 COMMENT '验证码IP每分钟上限',
  `verification_code_ip_hour_limit` int NOT NULL DEFAULT 20 COMMENT '验证码IP每小时上限',
  `verification_code_ip_day_limit` int NOT NULL DEFAULT 50 COMMENT '验证码IP每日上限',
  `register_ip_hour_limit` int NOT NULL DEFAULT 10 COMMENT '注册IP每小时上限',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (`id`),
  CONSTRAINT `chk_bp_auth_register_config_singleton` CHECK (`id` = 1)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='认证注册配置表';

CREATE TABLE IF NOT EXISTS `bp_captcha_provider_config` (
  `id` bigint NOT NULL AUTO_INCREMENT COMMENT '配置编号',
  `provider_key` varchar(64) NOT NULL COMMENT '验证码Provider：pow/geetest4/cloudflare',
  `scene` varchar(64) NOT NULL DEFAULT 'default' COMMENT '使用场景',
  `enabled` tinyint NOT NULL DEFAULT 1 COMMENT '是否启用',
  `site_key` varchar(255) DEFAULT NULL COMMENT '第三方站点Key',
  `secret_ref` varchar(255) DEFAULT NULL COMMENT '第三方密钥密文',
  `timeout_ms` int NOT NULL DEFAULT 3000 COMMENT '第三方校验超时',
  `failure_policy` varchar(32) NOT NULL DEFAULT 'deny' COMMENT '失败策略：固定deny（失败关闭）',
  `extra_config` json DEFAULT NULL COMMENT '扩展配置',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_bp_captcha_provider_scene` (`scene`, `provider_key`),
  KEY `idx_bp_captcha_provider_enabled` (`enabled`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='验证码Provider配置表';

CREATE TABLE IF NOT EXISTS `bp_auth_email_code` (
  `id` bigint NOT NULL AUTO_INCREMENT COMMENT '验证码编号',
  `scene` varchar(64) NOT NULL COMMENT '场景：register/password_reset/bind',
  `email` varchar(128) NOT NULL COMMENT '邮箱',
  `code_hash` varchar(128) NOT NULL COMMENT '验证码哈希',
  `expires_at` datetime NOT NULL COMMENT '过期时间',
  `consumed_at` datetime DEFAULT NULL COMMENT '消费时间',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  PRIMARY KEY (`id`),
  KEY `idx_bp_auth_email_code_scene_email` (`scene`, `email`, `expires_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='邮箱验证码表';

CREATE TABLE IF NOT EXISTS `bp_auth_mobile_code` (
  `id` bigint NOT NULL AUTO_INCREMENT COMMENT '验证码编号',
  `scene` varchar(64) NOT NULL COMMENT '场景：register/password_reset/bind',
  `mobile` varchar(32) NOT NULL COMMENT '手机号',
  `code_hash` varchar(128) NOT NULL COMMENT '验证码哈希',
  `expires_at` datetime NOT NULL COMMENT '过期时间',
  `consumed_at` datetime DEFAULT NULL COMMENT '消费时间',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  PRIMARY KEY (`id`),
  KEY `idx_bp_auth_mobile_code_scene_mobile` (`scene`, `mobile`, `expires_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='手机验证码表';

CREATE TABLE IF NOT EXISTS `bp_auth_refresh_token_audit` (
  `id` bigint NOT NULL AUTO_INCREMENT COMMENT '审计编号',
  `user_id` bigint NOT NULL COMMENT '用户编号',
  `jti` varchar(64) NOT NULL COMMENT 'Refresh Token JTI',
  `device_fingerprint` varchar(64) DEFAULT NULL COMMENT '设备指纹',
  `token_version` int NOT NULL DEFAULT 0 COMMENT '签发时Token版本',
  `issued_at` datetime NOT NULL COMMENT '签发时间',
  `expires_at` datetime NOT NULL COMMENT '过期时间',
  `revoked_at` datetime DEFAULT NULL COMMENT '吊销时间',
  `revoke_reason` varchar(64) DEFAULT NULL COMMENT '吊销原因',
  `last_used_at` datetime DEFAULT NULL COMMENT '最后使用时间',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_bp_auth_refresh_jti` (`jti`),
  KEY `idx_bp_auth_refresh_user` (`user_id`, `expires_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='Refresh Token审计表';

CREATE TABLE IF NOT EXISTS `bp_message_provider_config` (
  `id` bigint NOT NULL AUTO_INCREMENT COMMENT '配置编号',
  `channel` varchar(32) NOT NULL COMMENT '消息通道：email/sms',
  `provider_key` varchar(64) NOT NULL COMMENT 'Provider标识：smtp/aliyun/tencent等',
  `enabled` tinyint NOT NULL DEFAULT 0 COMMENT '是否启用',
  `config_ref` varchar(255) DEFAULT NULL COMMENT '配置引用或环境变量前缀',
  `extra_config` json DEFAULT NULL COMMENT '扩展配置',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_bp_message_provider_channel` (`channel`, `provider_key`),
  KEY `idx_bp_message_provider_enabled` (`enabled`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='消息Provider配置表';

CREATE TABLE IF NOT EXISTS `bp_user_specified_response` (
  `user_id` bigint NOT NULL COMMENT '用户编号',
  `enabled` tinyint NOT NULL DEFAULT 0 COMMENT '是否启用指定返回',
  `billable` tinyint NOT NULL DEFAULT 0 COMMENT '指定返回是否计费',
  `body` mediumtext COMMENT '指定返回内容',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (`user_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='用户指定返回配置表';

-- 初始管理员不在 SQL 中播种：首次启动时由 INITIAL_ADMIN_PASSWORD 环境变量创建，
-- 避免任何默认口令或口令哈希进入公开仓库。

INSERT INTO `bp_role` (`id`, `role_key`, `role_name`, `status`)
VALUES
  (1, 'admin', '系统管理员', 1),
  (2, 'user', '普通用户', 1)
ON DUPLICATE KEY UPDATE `role_name` = VALUES(`role_name`), `status` = VALUES(`status`);

INSERT INTO `bp_auth_register_config`
  (`id`, `register_enabled`, `default_user_qps`, `register_gift_points`, `email_register_enabled`, `mobile_register_enabled`, `verification_code_ip_minute_limit`, `verification_code_ip_hour_limit`, `verification_code_ip_day_limit`, `register_ip_hour_limit`)
VALUES
  (1, 1, 1, 0, 0, 0, 5, 20, 50, 10)
ON DUPLICATE KEY UPDATE
  `register_enabled` = VALUES(`register_enabled`),
  `default_user_qps` = VALUES(`default_user_qps`);

INSERT INTO `bp_captcha_provider_config`
  (`provider_key`, `scene`, `enabled`, `timeout_ms`, `failure_policy`, `extra_config`)
VALUES
  ('pow', 'default', 1, 3000, 'deny', JSON_OBJECT('difficulty', 3)),
  ('pow', 'login', 1, 3000, 'deny', JSON_OBJECT('difficulty', 3)),
  ('pow', 'register', 1, 3000, 'deny', JSON_OBJECT('difficulty', 3)),
  ('pow', 'register_email', 1, 3000, 'deny', JSON_OBJECT('difficulty', 3)),
  ('pow', 'register_mobile', 1, 3000, 'deny', JSON_OBJECT('difficulty', 3)),
  ('pow', 'password_reset', 1, 3000, 'deny', JSON_OBJECT('difficulty', 3)),
  ('pow', 'change_password', 1, 3000, 'deny', JSON_OBJECT('difficulty', 3))
ON DUPLICATE KEY UPDATE
  `enabled` = VALUES(`enabled`),
  `extra_config` = VALUES(`extra_config`);

INSERT INTO `bp_message_provider_config`
  (`channel`, `provider_key`, `enabled`, `config_ref`, `extra_config`)
VALUES
  ('email', 'smtp', 0, 'SMTP_*', JSON_OBJECT('official', false)),
  ('sms', 'aliyun', 0, 'SMS_*', JSON_OBJECT('officialSdkRequired', true)),
  ('sms', 'tencent', 0, 'SMS_*', JSON_OBJECT('officialSdkRequired', true))
ON DUPLICATE KEY UPDATE
  `config_ref` = VALUES(`config_ref`),
  `extra_config` = VALUES(`extra_config`);


-- ============================================================
-- Migration: 000002_rbac.up.sql
-- ============================================================

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


-- ============================================================
-- Migration: 000003_interface.up.sql
-- ============================================================

CREATE TABLE IF NOT EXISTS `bp_interface_api` (
  `id` bigint NOT NULL AUTO_INCREMENT COMMENT '接口编号',
  `api_code` varchar(64) NOT NULL COMMENT '开放接口唯一编码',
  `name` varchar(128) NOT NULL COMMENT '接口名称',
  `avatar_url` varchar(1024) DEFAULT NULL COMMENT '接口头像地址',
  `request_url` varchar(1024) NOT NULL COMMENT '接口请求地址模板',
  `request_method` varchar(16) NOT NULL COMMENT '请求方式：GET/POST/GET_POST',
  `price` decimal(12,4) NOT NULL DEFAULT 0.0000 COMMENT '余额单次价格',
  `point_price` bigint NOT NULL DEFAULT 0 COMMENT '点数单次价格',
  `is_top` tinyint NOT NULL DEFAULT 0 COMMENT '是否置顶',
  `is_featured` tinyint NOT NULL DEFAULT 0 COMMENT '是否精选',
  `status` tinyint NOT NULL DEFAULT 1 COMMENT '状态：1启用，0禁用',
  `remark` varchar(255) NOT NULL COMMENT '接口描述',
  `polling_enabled` tinyint NOT NULL DEFAULT 0 COMMENT '是否启用轮询',
  `polling_mode` varchar(32) NOT NULL DEFAULT 'SINGLE' COMMENT '轮询模式：SINGLE/ROUND_ROBIN/PRIMARY',
  `upstream_urls` json DEFAULT NULL COMMENT '上游接口配置',
  `polling_check_enabled` tinyint NOT NULL DEFAULT 0 COMMENT '主接口响应校验开关',
  `polling_check_field` varchar(128) DEFAULT NULL COMMENT '主接口响应校验字段',
  `polling_check_expected` varchar(255) DEFAULT NULL COMMENT '主接口响应校验期望值',
  `current_node` int NOT NULL DEFAULT 1 COMMENT '当前轮询节点',
  `doc_summary` varchar(500) DEFAULT NULL COMMENT '公开文档描述',
  `doc_response_type` varchar(16) NOT NULL DEFAULT 'JSON' COMMENT '公开文档响应类型',
  `doc_preferred_method` varchar(16) DEFAULT NULL COMMENT '公开文档推荐请求方式',
  `doc_request_params` json DEFAULT NULL COMMENT '公开文档请求参数',
  `doc_response_fields` json DEFAULT NULL COMMENT '公开文档响应字段',
  `doc_response_example` mediumtext COMMENT '公开文档响应示例',
  `doc_status_codes` json DEFAULT NULL COMMENT '公开文档状态码',
  `doc_notice` varchar(500) DEFAULT NULL COMMENT '公开文档提示',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_bp_interface_api_code` (`api_code`),
  KEY `idx_bp_interface_status` (`status`),
  KEY `idx_bp_interface_featured` (`is_featured`),
  KEY `idx_bp_interface_method` (`request_method`),
  KEY `idx_bp_interface_sort` (`is_top`, `is_featured`, `id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='API接口配置表';

CREATE TABLE IF NOT EXISTS `bp_interface_billing_rule` (
  `id` bigint NOT NULL AUTO_INCREMENT COMMENT '规则编号',
  `interface_id` bigint NOT NULL COMMENT '接口编号',
  `field_name` varchar(128) NOT NULL COMMENT '响应字段',
  `operator` varchar(16) NOT NULL COMMENT '比较符：EQ/NE/GT/LT/CONTAINS',
  `expected_value` varchar(255) NOT NULL COMMENT '期望值',
  `sort_no` int NOT NULL DEFAULT 1 COMMENT '排序',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  PRIMARY KEY (`id`),
  KEY `idx_bp_interface_billing_rule_interface` (`interface_id`, `sort_no`, `id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='API接口扣费规则表';

CREATE TABLE IF NOT EXISTS `bp_interface_call_log` (
  `id` bigint NOT NULL AUTO_INCREMENT COMMENT '日志编号',
  `interface_id` bigint NOT NULL COMMENT '接口编号',
  `user_id` bigint NOT NULL COMMENT '调用用户编号',
  `request_method` varchar(16) NOT NULL COMMENT '实际请求方式',
  `request_params` mediumtext COMMENT '请求参数',
  `request_value` varchar(500) DEFAULT NULL COMMENT '请求展示值',
  `response_status` int NOT NULL DEFAULT 0 COMMENT '响应状态码',
  `response_body` mediumtext COMMENT '响应内容',
  `success` tinyint NOT NULL DEFAULT 0 COMMENT '是否成功',
  `billable` tinyint NOT NULL DEFAULT 0 COMMENT '是否可计费',
  `charge_type` varchar(32) NOT NULL DEFAULT 'ADMIN' COMMENT '扣费类型',
  `charge_amount` decimal(12,4) NOT NULL DEFAULT 0.0000 COMMENT '扣费金额',
  `point_price` bigint NOT NULL DEFAULT 0 COMMENT '扣费点数',
  `elapsed_ms` bigint NOT NULL DEFAULT 0 COMMENT '耗时毫秒',
  `polling_mode` varchar(32) DEFAULT NULL COMMENT '轮询模式',
  `upstream_url` varchar(1024) DEFAULT NULL COMMENT '上游节点引用（兼容历史结构）',
  `upstream_switched` tinyint NOT NULL DEFAULT 0 COMMENT '是否发生上游切换',
  `error_message` varchar(500) DEFAULT NULL COMMENT '错误信息',
  `client_ip` varchar(64) DEFAULT NULL COMMENT '客户端IP',
  `client_region` varchar(128) DEFAULT NULL COMMENT '客户端地区',
  `user_visible` tinyint NOT NULL DEFAULT 1 COMMENT '用户是否可见',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  PRIMARY KEY (`id`),
  KEY `idx_bp_interface_call_log_user` (`user_id`, `created_at`),
  KEY `idx_bp_interface_call_log_interface` (`interface_id`, `created_at`),
  KEY `idx_bp_interface_call_log_success` (`success`, `created_at`),
  KEY `idx_bp_interface_call_log_charge` (`charge_type`, `created_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='API接口调用日志表';

INSERT INTO `bp_menu`
  (`id`, `pid`, `name`, `path`, `component`, `redirect`, `type`, `auth_code`, `meta`, `status`)
VALUES
  (40, 0, 'InterfaceManagement', '/interface', NULL, NULL, 'catalog', 'interface:root', JSON_OBJECT('title', 'system.interface.title', 'icon', 'lucide:plug', 'order', 9995), 1),
  (41, 40, 'InterfaceList', '/interface/list', '/interface/list', NULL, 'menu', 'interface:list', JSON_OBJECT('title', 'system.interface.listTitle', 'icon', 'lucide:list', 'order', 1), 1),
  (42, 41, 'InterfaceCreate', '', NULL, NULL, 'button', 'interface:create', JSON_OBJECT('title', '新增接口', 'order', 1), 1),
  (43, 41, 'InterfaceUpdate', '', NULL, NULL, 'button', 'interface:update', JSON_OBJECT('title', '编辑接口', 'order', 2), 1),
  (44, 41, 'InterfaceDelete', '', NULL, NULL, 'button', 'interface:delete', JSON_OBJECT('title', '删除接口', 'order', 3), 1),
  (45, 41, 'InterfaceDoc', '', NULL, NULL, 'button', 'interface:doc', JSON_OBJECT('title', '接口文档', 'order', 4), 1),
  (46, 41, 'InterfacePolling', '', NULL, NULL, 'button', 'interface:polling', JSON_OBJECT('title', '接口轮询', 'order', 5), 1),
  (47, 41, 'InterfaceBilling', '', NULL, NULL, 'button', 'interface:billing', JSON_OBJECT('title', '扣费规则', 'order', 6), 1),
  (48, 41, 'InterfaceInvoke', '', NULL, NULL, 'button', 'interface:invoke', JSON_OBJECT('title', '测试调用', 'order', 7), 1),
  (49, 41, 'InterfaceUploadAvatar', '', NULL, NULL, 'button', 'interface:upload-avatar', JSON_OBJECT('title', '上传接口头像', 'order', 8), 1),
  (50, 40, 'InterfaceCallLog', '/interface/log', '/interface/log', NULL, 'menu', 'interface:call-log:list', JSON_OBJECT('title', 'system.interface.callLogTitle', 'icon', 'lucide:history', 'order', 2), 1)
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
SELECT 1, id FROM `bp_menu` WHERE id BETWEEN 40 AND 50;

INSERT IGNORE INTO `bp_role_menu` (`role_id`, `menu_id`)
SELECT 2, id FROM `bp_menu` WHERE id IN (41, 50);


-- ============================================================
-- Migration: 000004_open_gateway.up.sql
-- ============================================================

CREATE TABLE IF NOT EXISTS `bp_api_key` (
  `id` bigint NOT NULL AUTO_INCREMENT COMMENT 'API Key编号',
  `user_id` bigint NOT NULL COMMENT '所属用户',
  `name` varchar(64) NOT NULL COMMENT 'Key名称',
  `key_hash` char(64) NOT NULL COMMENT 'Key SHA-256哈希',
  `key_prefix` varchar(16) NOT NULL COMMENT 'Key前缀',
  `key_suffix` varchar(8) NOT NULL COMMENT 'Key后四位',
  `key_type` varchar(16) NOT NULL DEFAULT 'GLOBAL' COMMENT 'Key类型：GLOBAL/INTERFACE',
  `status` tinyint NOT NULL DEFAULT 1 COMMENT '状态：1启用，0禁用',
  `expires_at` datetime DEFAULT NULL COMMENT '过期时间',
  `last_used_at` datetime DEFAULT NULL COMMENT '最后使用时间',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_bp_api_key_hash` (`key_hash`),
  KEY `idx_bp_api_key_user` (`user_id`, `status`),
  KEY `idx_bp_api_key_type` (`key_type`, `status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='开放接口API Key表';

CREATE TABLE IF NOT EXISTS `bp_api_key_interface` (
  `key_id` bigint NOT NULL COMMENT 'API Key编号',
  `interface_id` bigint NOT NULL COMMENT '接口编号',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  PRIMARY KEY (`key_id`, `interface_id`),
  KEY `idx_bp_api_key_interface_api` (`interface_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='API Key接口授权表';

CREATE TABLE IF NOT EXISTS `bp_api_key_ip_whitelist` (
  `id` bigint NOT NULL AUTO_INCREMENT COMMENT '白名单编号',
  `key_id` bigint NOT NULL COMMENT 'API Key编号',
  `ip_rule` varchar(64) NOT NULL COMMENT '单IP或CIDR',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_bp_api_key_ip_rule` (`key_id`, `ip_rule`),
  KEY `idx_bp_api_key_ip_key` (`key_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='API Key IP白名单表';

ALTER TABLE `bp_interface_call_log`
  ADD COLUMN `api_key_id` bigint DEFAULT NULL COMMENT '开放调用API Key编号' AFTER `user_id`,
  ADD COLUMN `api_code` varchar(64) DEFAULT NULL COMMENT '开放接口编码快照' AFTER `api_key_id`,
  ADD COLUMN `billing_status` varchar(32) NOT NULL DEFAULT 'NONE' COMMENT '计费状态：NONE/OPEN_PENDING' AFTER `charge_type`,
  ADD COLUMN `billing_snapshot` json DEFAULT NULL COMMENT '计费上下文快照' AFTER `billing_status`,
  ADD KEY `idx_bp_interface_call_log_key` (`api_key_id`, `created_at`),
  ADD KEY `idx_bp_interface_call_log_api_code` (`api_code`, `created_at`);

INSERT INTO `bp_menu`
  (`id`, `pid`, `name`, `path`, `component`, `redirect`, `type`, `auth_code`, `meta`, `status`)
VALUES
  (60, 0, 'ApiKeyManagement', '/key', NULL, NULL, 'catalog', 'key:root', JSON_OBJECT('title', 'system.key.title', 'icon', 'lucide:key-round', 'order', 9994), 1),
  (61, 60, 'ApiKeyList', '/key/list', '/key/list', NULL, 'menu', 'key:list', JSON_OBJECT('title', 'system.key.listTitle', 'icon', 'lucide:list-key', 'order', 1), 1),
  (62, 61, 'ApiKeyCreate', '', NULL, NULL, 'button', 'key:create', JSON_OBJECT('title', '新增Key', 'order', 1), 1),
  (63, 61, 'ApiKeyUpdate', '', NULL, NULL, 'button', 'key:update', JSON_OBJECT('title', '编辑Key', 'order', 2), 1),
  (64, 61, 'ApiKeyDelete', '', NULL, NULL, 'button', 'key:delete', JSON_OBJECT('title', '删除Key', 'order', 3), 1),
  (65, 61, 'ApiKeyStatus', '', NULL, NULL, 'button', 'key:status', JSON_OBJECT('title', 'Key状态', 'order', 4), 1),
  (66, 61, 'ApiKeyInterface', '', NULL, NULL, 'button', 'key:interface', JSON_OBJECT('title', '接口授权', 'order', 5), 1),
  (67, 61, 'ApiKeyIPWhitelist', '', NULL, NULL, 'button', 'key:ip-whitelist', JSON_OBJECT('title', 'IP白名单', 'order', 6), 1)
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
SELECT 1, id FROM `bp_menu` WHERE id BETWEEN 60 AND 67;

INSERT IGNORE INTO `bp_role_menu` (`role_id`, `menu_id`)
SELECT 2, id FROM `bp_menu` WHERE id BETWEEN 60 AND 67;


-- ============================================================
-- Migration: 000005_billing_package.up.sql
-- ============================================================

CREATE TABLE IF NOT EXISTS `bp_package_global` (
  `id` bigint NOT NULL AUTO_INCREMENT COMMENT '全站套餐编号',
  `name` varchar(128) NOT NULL COMMENT '套餐名称',
  `price` decimal(12,4) NOT NULL DEFAULT 0.0000 COMMENT '购买价格',
  `valid_days` int NOT NULL DEFAULT 30 COMMENT '有效天数',
  `daily_limit` bigint NOT NULL DEFAULT 0 COMMENT '每日调用上限，0不限',
  `qps_limit` int NOT NULL DEFAULT 0 COMMENT 'QPS上限，0不限',
  `status` tinyint NOT NULL DEFAULT 1 COMMENT '状态：1启用，0禁用',
  `remark` varchar(255) DEFAULT NULL COMMENT '备注',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  KEY `idx_bp_package_global_status` (`status`, `price`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='全站套餐表';

CREATE TABLE IF NOT EXISTS `bp_package_interface` (
  `id` bigint NOT NULL AUTO_INCREMENT COMMENT '接口套餐编号',
  `interface_id` bigint NOT NULL COMMENT '接口编号',
  `name` varchar(128) NOT NULL COMMENT '套餐名称',
  `status` tinyint NOT NULL DEFAULT 1 COMMENT '状态：1启用，0禁用',
  `remark` varchar(255) DEFAULT NULL COMMENT '备注',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  KEY `idx_bp_package_interface_api` (`interface_id`, `status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='接口套餐主表';

CREATE TABLE IF NOT EXISTS `bp_package_interface_spec` (
  `id` bigint NOT NULL AUTO_INCREMENT COMMENT '接口套餐规格编号',
  `package_id` bigint NOT NULL COMMENT '接口套餐编号',
  `name` varchar(128) NOT NULL COMMENT '规格名称',
  `price` decimal(12,4) NOT NULL DEFAULT 0.0000 COMMENT '购买价格',
  `valid_days` int NOT NULL DEFAULT 30 COMMENT '有效天数',
  `daily_limit` bigint NOT NULL DEFAULT 0 COMMENT '每日调用上限，0不限',
  `qps_limit` int NOT NULL DEFAULT 0 COMMENT 'QPS上限，0不限',
  `status` tinyint NOT NULL DEFAULT 1 COMMENT '状态：1启用，0禁用',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  KEY `idx_bp_package_interface_spec_pkg` (`package_id`, `status`, `price`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='接口套餐规格表';

CREATE TABLE IF NOT EXISTS `bp_package_point` (
  `id` bigint NOT NULL AUTO_INCREMENT COMMENT '点数套餐编号',
  `name` varchar(128) NOT NULL COMMENT '套餐名称',
  `price` decimal(12,4) NOT NULL DEFAULT 0.0000 COMMENT '购买价格',
  `points` bigint NOT NULL DEFAULT 0 COMMENT '购买点数',
  `status` tinyint NOT NULL DEFAULT 1 COMMENT '状态：1启用，0禁用',
  `remark` varchar(255) DEFAULT NULL COMMENT '备注',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  KEY `idx_bp_package_point_status` (`status`, `price`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='点数套餐表';

CREATE TABLE IF NOT EXISTS `bp_user_package_global` (
  `id` bigint NOT NULL AUTO_INCREMENT COMMENT '用户全站套餐权益编号',
  `user_id` bigint NOT NULL COMMENT '用户编号',
  `package_id` bigint NOT NULL COMMENT '购买套餐编号',
  `package_name` varchar(128) NOT NULL COMMENT '套餐名称快照',
  `daily_limit` bigint NOT NULL DEFAULT 0 COMMENT '每日调用上限快照，0不限',
  `qps_limit` int NOT NULL DEFAULT 0 COMMENT 'QPS上限快照，0不限',
  `start_at` datetime NOT NULL COMMENT '开始时间',
  `end_at` datetime NOT NULL COMMENT '结束时间',
  `day_key` date DEFAULT NULL COMMENT '当前统计日期',
  `day_used` bigint NOT NULL DEFAULT 0 COMMENT '当日已用',
  `total_used` bigint NOT NULL DEFAULT 0 COMMENT '总已用',
  `status` tinyint NOT NULL DEFAULT 1 COMMENT '状态：1启用，0禁用',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  KEY `idx_bp_user_package_global_available` (`user_id`, `status`, `end_at`, `id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='用户全站套餐权益表';

CREATE TABLE IF NOT EXISTS `bp_user_package_interface` (
  `id` bigint NOT NULL AUTO_INCREMENT COMMENT '用户接口套餐权益编号',
  `user_id` bigint NOT NULL COMMENT '用户编号',
  `interface_id` bigint NOT NULL COMMENT '接口编号',
  `package_id` bigint NOT NULL COMMENT '接口套餐编号',
  `spec_id` bigint NOT NULL COMMENT '接口套餐规格编号',
  `package_name` varchar(128) NOT NULL COMMENT '套餐名称快照',
  `spec_name` varchar(128) NOT NULL COMMENT '规格名称快照',
  `daily_limit` bigint NOT NULL DEFAULT 0 COMMENT '每日调用上限快照，0不限',
  `qps_limit` int NOT NULL DEFAULT 0 COMMENT 'QPS上限快照，0不限',
  `start_at` datetime NOT NULL COMMENT '开始时间',
  `end_at` datetime NOT NULL COMMENT '结束时间',
  `day_key` date DEFAULT NULL COMMENT '当前统计日期',
  `day_used` bigint NOT NULL DEFAULT 0 COMMENT '当日已用',
  `total_used` bigint NOT NULL DEFAULT 0 COMMENT '总已用',
  `status` tinyint NOT NULL DEFAULT 1 COMMENT '状态：1启用，0禁用',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  KEY `idx_bp_user_package_interface_available` (`user_id`, `interface_id`, `status`, `end_at`, `id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='用户接口套餐权益表';

CREATE TABLE IF NOT EXISTS `bp_billing_record` (
  `id` bigint NOT NULL AUTO_INCREMENT COMMENT '计费记录编号',
  `call_log_id` bigint DEFAULT NULL COMMENT '调用日志编号',
  `user_id` bigint NOT NULL COMMENT '用户编号',
  `api_key_id` bigint DEFAULT NULL COMMENT 'API Key编号',
  `interface_id` bigint NOT NULL COMMENT '接口编号',
  `api_code` varchar(64) NOT NULL COMMENT '接口编码快照',
  `charge_type` varchar(32) NOT NULL COMMENT '扣费类型',
  `charge_amount` decimal(12,4) NOT NULL DEFAULT 0.0000 COMMENT '余额扣费金额',
  `point_amount` bigint NOT NULL DEFAULT 0 COMMENT '点数扣费数量',
  `package_record_id` bigint DEFAULT NULL COMMENT '套餐权益记录编号',
  `status` varchar(32) NOT NULL DEFAULT 'RESERVED' COMMENT 'RESERVED/CONFIRMED/RELEASED/FAILED',
  `reason` varchar(255) DEFAULT NULL COMMENT '原因',
  `snapshot` json DEFAULT NULL COMMENT '计费快照',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  KEY `idx_bp_billing_record_user` (`user_id`, `created_at`),
  KEY `idx_bp_billing_record_call` (`call_log_id`),
  KEY `idx_bp_billing_record_status` (`status`, `created_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='开放调用计费记录表';

CREATE TABLE IF NOT EXISTS `bp_wallet_transaction` (
  `id` bigint NOT NULL AUTO_INCREMENT COMMENT '钱包流水编号',
  `user_id` bigint NOT NULL COMMENT '用户编号',
  `biz_type` varchar(32) NOT NULL COMMENT '业务类型',
  `biz_id` bigint DEFAULT NULL COMMENT '业务编号',
  `amount` decimal(12,4) NOT NULL DEFAULT 0.0000 COMMENT '余额变动',
  `points` bigint NOT NULL DEFAULT 0 COMMENT '点数变动',
  `balance_after` decimal(12,4) NOT NULL DEFAULT 0.0000 COMMENT '变动后余额',
  `points_after` bigint NOT NULL DEFAULT 0 COMMENT '变动后点数',
  `remark` varchar(255) DEFAULT NULL COMMENT '备注',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  KEY `idx_bp_wallet_transaction_user` (`user_id`, `created_at`),
  KEY `idx_bp_wallet_transaction_biz` (`biz_type`, `biz_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='用户余额点数流水表';

ALTER TABLE `bp_interface_call_log`
  ADD COLUMN `billing_record_id` bigint DEFAULT NULL COMMENT '计费记录编号' AFTER `billing_snapshot`,
  ADD KEY `idx_bp_interface_call_log_billing_record` (`billing_record_id`);

INSERT INTO `bp_menu`
  (`id`, `pid`, `name`, `path`, `component`, `redirect`, `type`, `auth_code`, `meta`, `status`)
VALUES
  (70, 0, 'PackageManagement', '/package', NULL, NULL, 'catalog', 'package:root', JSON_OBJECT('title', 'system.package.title', 'icon', 'lucide:boxes', 'order', 9993), 1),
  (71, 70, 'GlobalPackageList', '/package/global', '/package/global/list', NULL, 'menu', 'package:global:list', JSON_OBJECT('title', '全站套餐', 'order', 1), 1),
  (72, 71, 'GlobalPackageCreate', '', NULL, NULL, 'button', 'package:global:create', JSON_OBJECT('title', '新增全站套餐'), 1),
  (73, 71, 'GlobalPackageUpdate', '', NULL, NULL, 'button', 'package:global:update', JSON_OBJECT('title', '编辑全站套餐'), 1),
  (74, 71, 'GlobalPackageDelete', '', NULL, NULL, 'button', 'package:global:delete', JSON_OBJECT('title', '删除全站套餐'), 1),
  (75, 70, 'InterfacePackageList', '/package/interface', '/package/interface/list', NULL, 'menu', 'package:interface:list', JSON_OBJECT('title', '接口套餐', 'order', 2), 1),
  (76, 75, 'InterfacePackageCreate', '', NULL, NULL, 'button', 'package:interface:create', JSON_OBJECT('title', '新增接口套餐'), 1),
  (77, 75, 'InterfacePackageUpdate', '', NULL, NULL, 'button', 'package:interface:update', JSON_OBJECT('title', '编辑接口套餐'), 1),
  (78, 75, 'InterfacePackageDelete', '', NULL, NULL, 'button', 'package:interface:delete', JSON_OBJECT('title', '删除接口套餐'), 1),
  (79, 70, 'PointPackageList', '/package/point', '/package/point/list', NULL, 'menu', 'package:point:list', JSON_OBJECT('title', '点数套餐', 'order', 3), 1),
  (80, 79, 'PointPackageCreate', '', NULL, NULL, 'button', 'package:point:create', JSON_OBJECT('title', '新增点数套餐'), 1),
  (81, 79, 'PointPackageUpdate', '', NULL, NULL, 'button', 'package:point:update', JSON_OBJECT('title', '编辑点数套餐'), 1),
  (82, 79, 'PointPackageDelete', '', NULL, NULL, 'button', 'package:point:delete', JSON_OBJECT('title', '删除点数套餐'), 1),
  (83, 70, 'BillingRecordList', '/package/billing-records', '/interface/log', NULL, 'menu', 'billing:record:list', JSON_OBJECT('title', '计费记录', 'order', 4), 1),
  (84, 0, 'PackageMall', '/package/mall', NULL, NULL, 'catalog', 'package:mall:list', JSON_OBJECT('title', '套餐商城', 'icon', 'lucide:shopping-bag', 'order', 9992), 1),
  (85, 84, 'PackageMallPurchase', '', NULL, NULL, 'button', 'package:mall:purchase', JSON_OBJECT('title', '购买套餐'), 1)
ON DUPLICATE KEY UPDATE
  `pid` = VALUES(`pid`),
  `path` = VALUES(`path`),
  `component` = VALUES(`component`),
  `type` = VALUES(`type`),
  `auth_code` = VALUES(`auth_code`),
  `meta` = VALUES(`meta`),
  `status` = VALUES(`status`);

INSERT INTO `bp_menu`
  (`id`, `pid`, `name`, `path`, `component`, `redirect`, `type`, `auth_code`, `meta`, `status`)
VALUES
  (86, 84, 'UserGlobalPackage', '/purchase/global', '/purchase/global/list', NULL, 'menu', 'package:mall:list', JSON_OBJECT('title', 'system.package.buyGlobalTitle', 'icon', 'mdi:shopping-outline', 'order', 1), 1),
  (87, 84, 'UserInterfacePackage', '/purchase/interface', '/purchase/interface/list', NULL, 'menu', 'package:mall:list', JSON_OBJECT('title', 'system.package.buyInterfaceTitle', 'icon', 'mdi:shopping-outline', 'order', 2), 1),
  (88, 84, 'UserPointPackage', '/purchase/point', '/purchase/point/list', NULL, 'menu', 'package:mall:list', JSON_OBJECT('title', 'system.package.buyPointTitle', 'icon', 'mdi:database-plus', 'order', 3), 1),
  (89, 84, 'UserPaymentOrder', '/purchase/orders', '/payment/order/user', NULL, 'menu', 'package:mall:list', JSON_OBJECT('title', 'Payment orders', 'icon', 'mdi:receipt-clock-outline', 'order', 4), 1)
ON DUPLICATE KEY UPDATE
  `pid` = VALUES(`pid`),
  `path` = VALUES(`path`),
  `component` = VALUES(`component`),
  `type` = VALUES(`type`),
  `auth_code` = VALUES(`auth_code`),
  `meta` = VALUES(`meta`),
  `status` = VALUES(`status`);

INSERT IGNORE INTO `bp_role_menu` (`role_id`, `menu_id`)
SELECT 1, id FROM `bp_menu` WHERE id BETWEEN 70 AND 89;

INSERT IGNORE INTO `bp_role_menu` (`role_id`, `menu_id`)
SELECT 2, id FROM `bp_menu` WHERE id IN (84, 85, 86, 87, 88, 89);

INSERT INTO `bp_package_global` (`id`, `name`, `price`, `valid_days`, `daily_limit`, `qps_limit`, `status`, `remark`)
VALUES (1, '基础全站套餐', 0.0000, 30, 100, 1, 1, '默认演示套餐')
ON DUPLICATE KEY UPDATE `name` = VALUES(`name`);

INSERT INTO `bp_package_point` (`id`, `name`, `price`, `points`, `status`, `remark`)
VALUES (1, '基础点数包', 0.0000, 100, 1, '默认演示点数包')
ON DUPLICATE KEY UPDATE `name` = VALUES(`name`);


-- ============================================================
-- Migration: 000006_observability_stats.up.sql
-- ============================================================

ALTER TABLE `bp_interface_call_log`
  ADD COLUMN `api_key_prefix` varchar(16) DEFAULT NULL COMMENT 'API Key prefix snapshot' AFTER `api_key_id`,
  ADD COLUMN `request_size` bigint NOT NULL DEFAULT 0 COMMENT 'request size bytes' AFTER `request_value`,
  ADD COLUMN `response_size` bigint NOT NULL DEFAULT 0 COMMENT 'response body saved size bytes' AFTER `response_body`,
  ADD COLUMN `ip_country` varchar(64) DEFAULT NULL COMMENT 'IP country' AFTER `client_region`,
  ADD COLUMN `ip_province` varchar(64) DEFAULT NULL COMMENT 'IP province' AFTER `ip_country`,
  ADD COLUMN `ip_city` varchar(64) DEFAULT NULL COMMENT 'IP city' AFTER `ip_province`,
  ADD COLUMN `ip_isp` varchar(128) DEFAULT NULL COMMENT 'IP ISP' AFTER `ip_city`,
  ADD COLUMN `latitude` decimal(10,6) DEFAULT NULL COMMENT 'IP latitude' AFTER `ip_isp`,
  ADD COLUMN `longitude` decimal(10,6) DEFAULT NULL COMMENT 'IP longitude' AFTER `latitude`,
  ADD COLUMN `region_provider` varchar(32) DEFAULT NULL COMMENT 'IP region provider' AFTER `longitude`,
  ADD COLUMN `trace_id` varchar(64) DEFAULT NULL COMMENT 'gateway trace id' AFTER `region_provider`,
  ADD KEY `idx_bp_interface_call_log_region` (`ip_country`, `ip_province`, `ip_city`, `created_at`),
  ADD KEY `idx_bp_interface_call_log_trace` (`trace_id`),
  ADD KEY `idx_bp_interface_call_log_billing_status` (`billing_status`, `created_at`);

CREATE TABLE IF NOT EXISTS `bp_user_log_cleanup_config` (
  `user_id` bigint NOT NULL COMMENT 'user id',
  `retention_days` int NOT NULL DEFAULT 90 COMMENT 'visible log retention days, 0 means keep forever',
  `auto_hide_enabled` tinyint NOT NULL DEFAULT 0 COMMENT 'auto hide old user-visible logs',
  `last_cleanup_at` datetime DEFAULT NULL COMMENT 'last cleanup time',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`user_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='user log cleanup config';

INSERT INTO `bp_menu`
  (`id`, `pid`, `name`, `path`, `component`, `redirect`, `type`, `auth_code`, `meta`, `status`)
VALUES
  (90, 0, 'DashboardStats', '/dashboard-stats', NULL, NULL, 'catalog', 'dashboard:root', JSON_OBJECT('title', '数据看板', 'icon', 'lucide:chart-line', 'order', 9991), 1),
  (91, 90, 'AdminDashboardStats', '/dashboard-stats/admin', '/dashboard/workspace/index', NULL, 'menu', 'dashboard:admin:view', JSON_OBJECT('title', '管理看板', 'order', 1), 1),
  (92, 90, 'UserDashboardStats', '/dashboard-stats/user', '/dashboard/workspace/index', NULL, 'menu', 'dashboard:user:view', JSON_OBJECT('title', '用户看板', 'order', 2), 1),
  (93, 90, 'CallLogDetail', '', NULL, NULL, 'button', 'log:call:detail', JSON_OBJECT('title', '调用日志详情', 'order', 3), 1),
  (94, 90, 'CallLogExport', '', NULL, NULL, 'button', 'log:call:export', JSON_OBJECT('title', '导出调用日志', 'order', 4), 1),
  (95, 90, 'CallLogCleanup', '', NULL, NULL, 'button', 'log:call:cleanup', JSON_OBJECT('title', '清理调用日志', 'order', 5), 1),
  (96, 90, 'CallLogListPermission', '', NULL, NULL, 'button', 'log:call:list', JSON_OBJECT('title', '调用日志列表', 'order', 6), 1)
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
SELECT 1, id FROM `bp_menu` WHERE id BETWEEN 90 AND 96;

INSERT IGNORE INTO `bp_role_menu` (`role_id`, `menu_id`)
SELECT 2, id FROM `bp_menu` WHERE id IN (92, 93);


-- ============================================================
-- Migration: 000007_payment_alipay.up.sql
-- ============================================================

CREATE TABLE IF NOT EXISTS `bp_payment_alipay_config` (
  `id` bigint NOT NULL DEFAULT 1 COMMENT 'singleton id',
  `mode` varchar(16) NOT NULL DEFAULT 'SANDBOX' COMMENT 'SANDBOX/PRODUCTION',
  `app_id` varchar(64) NOT NULL DEFAULT '',
  `gateway_url` varchar(255) NOT NULL DEFAULT '',
  `app_private_key_cipher` text,
  `alipay_public_key` text,
  `seller_id` varchar(64) DEFAULT NULL,
  `notify_url` varchar(512) DEFAULT NULL,
  `return_url` varchar(512) DEFAULT NULL,
  `enabled` tinyint NOT NULL DEFAULT 0,
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='Alipay payment config';

INSERT INTO `bp_payment_alipay_config` (`id`, `mode`, `gateway_url`, `enabled`)
VALUES (1, 'SANDBOX', 'https://openapi-sandbox.dl.alipaydev.com/gateway.do', 0)
ON DUPLICATE KEY UPDATE `id` = `id`;

CREATE TABLE IF NOT EXISTS `bp_recharge_preset` (
  `id` bigint NOT NULL AUTO_INCREMENT,
  `name` varchar(128) NOT NULL,
  `amount` decimal(12,4) NOT NULL DEFAULT 0.0000,
  `gift_amount` decimal(12,4) NOT NULL DEFAULT 0.0000,
  `sort_order` int NOT NULL DEFAULT 0,
  `status` tinyint NOT NULL DEFAULT 1,
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  KEY `idx_bp_recharge_preset_status_sort` (`status`, `sort_order`, `id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='Recharge preset';

CREATE TABLE IF NOT EXISTS `bp_payment_order` (
  `id` bigint NOT NULL AUTO_INCREMENT,
  `order_no` varchar(64) NOT NULL,
  `user_id` bigint NOT NULL,
  `order_type` varchar(32) NOT NULL COMMENT 'RECHARGE/GLOBAL_PACKAGE/INTERFACE_PACKAGE/POINT_PACKAGE',
  `product` varchar(16) NOT NULL COMMENT 'PAGE/WAP/FACE',
  `subject` varchar(128) NOT NULL,
  `target_id` bigint DEFAULT NULL,
  `pay_amount` decimal(12,4) NOT NULL DEFAULT 0.0000,
  `gift_amount` decimal(12,4) NOT NULL DEFAULT 0.0000,
  `grant_points` bigint NOT NULL DEFAULT 0,
  `status` varchar(32) NOT NULL DEFAULT 'CREATED',
  `alipay_trade_no` varchar(128) DEFAULT NULL,
  `buyer_id` varchar(64) DEFAULT NULL,
  `pay_form` mediumtext,
  `pay_url` varchar(1024) DEFAULT NULL,
  `qr_code` varchar(1024) DEFAULT NULL,
  `expire_at` datetime NOT NULL,
  `paid_at` datetime DEFAULT NULL,
  `settled_at` datetime DEFAULT NULL,
  `snapshot` json DEFAULT NULL,
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_bp_payment_order_no` (`order_no`),
  KEY `idx_bp_payment_order_user` (`user_id`, `created_at`),
  KEY `idx_bp_payment_order_status` (`status`, `created_at`),
  KEY `idx_bp_payment_order_trade` (`alipay_trade_no`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='Payment order';

CREATE TABLE IF NOT EXISTS `bp_payment_notify_log` (
  `id` bigint NOT NULL AUTO_INCREMENT,
  `order_no` varchar(64) DEFAULT NULL,
  `trade_no` varchar(128) DEFAULT NULL,
  `notify_id` varchar(128) DEFAULT NULL,
  `event_type` varchar(32) NOT NULL DEFAULT 'ALIPAY_NOTIFY',
  `verified` tinyint NOT NULL DEFAULT 0,
  `processed` tinyint NOT NULL DEFAULT 0,
  `message` varchar(255) DEFAULT NULL,
  `payload` json DEFAULT NULL,
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  KEY `idx_bp_payment_notify_order` (`order_no`, `created_at`),
  KEY `idx_bp_payment_notify_trade` (`trade_no`, `created_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='Payment notify log';

INSERT INTO `bp_menu`
  (`id`, `pid`, `name`, `path`, `component`, `redirect`, `type`, `auth_code`, `meta`, `status`)
VALUES
  (100, 0, 'PaymentManagement', '/payment', NULL, NULL, 'catalog', 'payment:root', JSON_OBJECT('title', 'Payment', 'icon', 'lucide:credit-card', 'order', 9990), 1),
  (101, 100, 'PaymentConfig', '/payment/config', '/payment/config/index', NULL, 'menu', 'payment:config:view', JSON_OBJECT('title', 'Alipay Config', 'order', 1), 1),
  (102, 101, 'PaymentConfigUpdate', '', NULL, NULL, 'button', 'payment:config:update', JSON_OBJECT('title', 'Update Alipay Config'), 1),
  (103, 100, 'RechargePresetList', '/payment/amounts', '/payment/amount/index', NULL, 'menu', 'payment:preset:list', JSON_OBJECT('title', 'Recharge Presets', 'order', 2), 1),
  (104, 103, 'RechargePresetCreate', '', NULL, NULL, 'button', 'payment:preset:create', JSON_OBJECT('title', 'Create Recharge Preset'), 1),
  (105, 103, 'RechargePresetUpdate', '', NULL, NULL, 'button', 'payment:preset:update', JSON_OBJECT('title', 'Update Recharge Preset'), 1),
  (106, 103, 'RechargePresetDelete', '', NULL, NULL, 'button', 'payment:preset:delete', JSON_OBJECT('title', 'Delete Recharge Preset'), 1),
  (107, 100, 'PaymentOrderList', '/payment/orders', '/payment/order/index', NULL, 'menu', 'payment:order:list', JSON_OBJECT('title', 'Payment Orders', 'order', 3), 1),
  (108, 107, 'PaymentOrderDetail', '', NULL, NULL, 'button', 'payment:order:detail', JSON_OBJECT('title', 'Payment Order Detail'), 1),
  (109, 107, 'PaymentOrderSync', '', NULL, NULL, 'button', 'payment:order:sync', JSON_OBJECT('title', 'Sync Payment Order'), 1)
ON DUPLICATE KEY UPDATE
  `pid` = VALUES(`pid`),
  `path` = VALUES(`path`),
  `component` = VALUES(`component`),
  `type` = VALUES(`type`),
  `auth_code` = VALUES(`auth_code`),
  `meta` = VALUES(`meta`),
  `status` = VALUES(`status`);

INSERT IGNORE INTO `bp_role_menu` (`role_id`, `menu_id`)
SELECT 1, id FROM `bp_menu` WHERE id BETWEEN 100 AND 109;

INSERT INTO `bp_recharge_preset` (`id`, `name`, `amount`, `gift_amount`, `sort_order`, `status`)
VALUES
  (1, 'Recharge 50', 50.0000, 0.0000, 1, 1),
  (2, 'Recharge 100', 100.0000, 5.0000, 2, 1)
ON DUPLICATE KEY UPDATE `name` = VALUES(`name`);


-- ============================================================
-- Migration: 000008_content_ops.up.sql
-- ============================================================

CREATE TABLE IF NOT EXISTS `bp_notice` (
  `id` bigint NOT NULL AUTO_INCREMENT,
  `title` varchar(100) NOT NULL,
  `summary` varchar(255) NOT NULL DEFAULT '',
  `content_html` mediumtext NOT NULL,
  `is_top` tinyint NOT NULL DEFAULT 0,
  `is_popup` tinyint NOT NULL DEFAULT 0,
  `status` tinyint NOT NULL DEFAULT 1,
  `publish_time` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `creator_id` bigint DEFAULT NULL,
  `updater_id` bigint DEFAULT NULL,
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  KEY `idx_bp_notice_public` (`status`, `is_top`, `publish_time`, `id`),
  KEY `idx_bp_notice_popup` (`status`, `is_popup`, `publish_time`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='Content notice';

CREATE TABLE IF NOT EXISTS `bp_home_notice_config` (
  `id` bigint NOT NULL DEFAULT 1,
  `enabled` tinyint NOT NULL DEFAULT 0,
  `content` varchar(300) NOT NULL DEFAULT '',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='Home rolling notice config';

INSERT INTO `bp_home_notice_config` (`id`, `enabled`, `content`)
VALUES (1, 0, '')
ON DUPLICATE KEY UPDATE `id` = `id`;

CREATE TABLE IF NOT EXISTS `bp_site_config` (
  `id` bigint NOT NULL DEFAULT 1,
  `site_name` varchar(64) NOT NULL DEFAULT 'BugPk-Api',
  `logo_url` varchar(1024) NOT NULL DEFAULT '',
  `slogan` varchar(120) NOT NULL DEFAULT '',
  `description` varchar(255) NOT NULL DEFAULT '',
  `contact_email` varchar(128) NOT NULL DEFAULT '',
  `contact_phone` varchar(64) NOT NULL DEFAULT '',
  `contact_qq` varchar(64) NOT NULL DEFAULT '',
  `contact_wechat` varchar(64) NOT NULL DEFAULT '',
  `contact_address` varchar(255) NOT NULL DEFAULT '',
  `icp` varchar(128) NOT NULL DEFAULT '',
  `copyright` varchar(128) NOT NULL DEFAULT '',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='Site config singleton';

INSERT INTO `bp_site_config` (`id`, `site_name`, `slogan`, `description`, `copyright`)
VALUES (1, 'BugPk-Api', 'Open API service platform', 'BugPk-Api service platform', 'Copyright BugPk-Api')
ON DUPLICATE KEY UPDATE `id` = `id`;

CREATE TABLE IF NOT EXISTS `bp_friend_link` (
  `id` bigint NOT NULL AUTO_INCREMENT,
  `site_name` varchar(100) NOT NULL,
  `site_url` varchar(1024) NOT NULL,
  `normalized_site_url` varchar(1024) NOT NULL,
  `logo_url` varchar(1024) NOT NULL DEFAULT '',
  `description` varchar(255) NOT NULL DEFAULT '',
  `sort_no` int NOT NULL DEFAULT 0,
  `status` tinyint NOT NULL DEFAULT 1,
  `applicant_name` varchar(64) NOT NULL DEFAULT '',
  `applicant_email` varchar(128) NOT NULL DEFAULT '',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_bp_friend_link_normalized_url` (`normalized_site_url`(191)),
  KEY `idx_bp_friend_link_public` (`status`, `sort_no`, `id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='Friend link';

CREATE TABLE IF NOT EXISTS `bp_friend_link_application` (
  `id` bigint NOT NULL AUTO_INCREMENT,
  `user_id` bigint NOT NULL,
  `username` varchar(64) NOT NULL DEFAULT '',
  `site_name` varchar(100) NOT NULL,
  `site_url` varchar(1024) NOT NULL,
  `normalized_site_url` varchar(1024) NOT NULL,
  `logo_url` varchar(1024) NOT NULL DEFAULT '',
  `description` varchar(255) NOT NULL DEFAULT '',
  `contact_name` varchar(64) NOT NULL DEFAULT '',
  `contact_email` varchar(128) NOT NULL DEFAULT '',
  `contact_qq` varchar(64) NOT NULL DEFAULT '',
  `backlink_url` varchar(1024) NOT NULL DEFAULT '',
  `status` varchar(16) NOT NULL DEFAULT 'PENDING',
  `reject_reason` varchar(255) NOT NULL DEFAULT '',
  `reviewer_id` bigint DEFAULT NULL,
  `reviewer_name` varchar(64) NOT NULL DEFAULT '',
  `review_time` datetime DEFAULT NULL,
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  KEY `idx_bp_friend_link_app_user` (`user_id`, `created_at`),
  KEY `idx_bp_friend_link_app_status` (`status`, `created_at`),
  KEY `idx_bp_friend_link_app_url` (`normalized_site_url`(191), `status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='Friend link application';

CREATE TABLE IF NOT EXISTS `bp_friend_link_config` (
  `id` bigint NOT NULL DEFAULT 1,
  `apply_enabled` tinyint NOT NULL DEFAULT 1,
  `apply_notice` varchar(500) NOT NULL DEFAULT '',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='Friend link config';

INSERT INTO `bp_friend_link_config` (`id`, `apply_enabled`, `apply_notice`)
VALUES (1, 1, '')
ON DUPLICATE KEY UPDATE `id` = `id`;

INSERT INTO `bp_menu`
  (`id`, `pid`, `name`, `path`, `component`, `redirect`, `type`, `auth_code`, `meta`, `status`)
VALUES
  (120, 0, 'ContentOperations', '/content', NULL, NULL, 'catalog', 'content:root', JSON_OBJECT('title', '内容运营', 'icon', 'lucide:newspaper', 'order', 9990), 1),
  (121, 120, 'NoticeList', '/content/notices', '/notice/admin/list', NULL, 'menu', 'notice:list', JSON_OBJECT('title', '公告管理', 'order', 1), 1),
  (122, 121, 'NoticeCreate', '', NULL, NULL, 'button', 'notice:create', JSON_OBJECT('title', '新增公告'), 1),
  (123, 121, 'NoticeUpdate', '', NULL, NULL, 'button', 'notice:update', JSON_OBJECT('title', '编辑公告'), 1),
  (124, 121, 'NoticeDelete', '', NULL, NULL, 'button', 'notice:delete', JSON_OBJECT('title', '删除公告'), 1),
  (125, 121, 'NoticeStatus', '', NULL, NULL, 'button', 'notice:status', JSON_OBJECT('title', '公告状态'), 1),
  (126, 121, 'NoticeTop', '', NULL, NULL, 'button', 'notice:top', JSON_OBJECT('title', '公告置顶'), 1),
  (127, 121, 'NoticePopup', '', NULL, NULL, 'button', 'notice:popup', JSON_OBJECT('title', '弹窗公告'), 1),
  (128, 120, 'HomeNoticeConfig', '/content/home-notice', '/notice/admin/home-notice/index', NULL, 'menu', 'notice:home-config', JSON_OBJECT('title', '首页滚动公告', 'order', 2), 1),
  (129, 121, 'NoticeImageUpload', '', NULL, NULL, 'button', 'notice:upload-image', JSON_OBJECT('title', '上传公告图片'), 1),
  (130, 120, 'SiteConfig', '/content/site', '/system/site/index', NULL, 'menu', 'site:config:view', JSON_OBJECT('title', '站点配置', 'order', 3), 1),
  (131, 130, 'SiteConfigUpdate', '', NULL, NULL, 'button', 'site:config:update', JSON_OBJECT('title', '更新站点配置'), 1),
  (132, 130, 'SiteLogoUpload', '', NULL, NULL, 'button', 'site:upload-logo', JSON_OBJECT('title', '上传站点Logo'), 1),
  (133, 120, 'FriendLinkList', '/content/friend-links', '/friend-link/list', NULL, 'menu', 'friend-link:list', JSON_OBJECT('title', '友情链接', 'order', 4), 1),
  (134, 133, 'FriendLinkCreate', '', NULL, NULL, 'button', 'friend-link:create', JSON_OBJECT('title', '新增友情链接'), 1),
  (135, 133, 'FriendLinkUpdate', '', NULL, NULL, 'button', 'friend-link:update', JSON_OBJECT('title', '编辑友情链接'), 1),
  (136, 133, 'FriendLinkDelete', '', NULL, NULL, 'button', 'friend-link:delete', JSON_OBJECT('title', '删除友情链接'), 1),
  (137, 133, 'FriendLinkStatus', '', NULL, NULL, 'button', 'friend-link:status', JSON_OBJECT('title', '友情链接状态'), 1),
  (138, 120, 'FriendLinkApplications', '/content/friend-link-applications', '/friend-link/applications', NULL, 'menu', 'friend-link:application:list', JSON_OBJECT('title', '友链申请', 'order', 5), 1),
  (139, 138, 'FriendLinkApplicationReview', '', NULL, NULL, 'button', 'friend-link:application:review', JSON_OBJECT('title', '审核友链申请'), 1),
  (140, 120, 'FriendLinkConfig', '/content/friend-link-config', '/friend-link/config', NULL, 'menu', 'friend-link:config:update', JSON_OBJECT('title', '友链配置', 'order', 6), 1),
  (141, 0, 'HomeOverview', '/home', NULL, NULL, 'catalog', 'home:overview:view', JSON_OBJECT('title', '首页概览', 'icon', 'lucide:layout-dashboard', 'order', 9989), 1)
ON DUPLICATE KEY UPDATE
  `pid` = VALUES(`pid`),
  `path` = VALUES(`path`),
  `component` = VALUES(`component`),
  `type` = VALUES(`type`),
  `auth_code` = VALUES(`auth_code`),
  `meta` = VALUES(`meta`),
  `status` = VALUES(`status`);

INSERT IGNORE INTO `bp_role_menu` (`role_id`, `menu_id`)
SELECT 1, id FROM `bp_menu` WHERE id BETWEEN 120 AND 141;


-- ============================================================
-- Migration: 000009_brand_bugpk_api.up.sql
-- ============================================================

-- 为全新安装补齐站点基础信息；已有自定义值的字段保持不变。
UPDATE `bp_site_config`
SET
  `site_name` = CASE WHEN TRIM(`site_name`) = '' THEN 'BugPk-Api' ELSE `site_name` END,
  `slogan` = CASE WHEN TRIM(`slogan`) = '' THEN 'Open API service platform' ELSE `slogan` END,
  `description` = CASE WHEN TRIM(`description`) = '' THEN 'BugPk-Api service platform' ELSE `description` END,
  `copyright` = CASE WHEN TRIM(`copyright`) = '' THEN 'Copyright BugPk-Api' ELSE `copyright` END
WHERE `id` = 1;


-- ============================================================
-- Migration: 000010_frontend_route_compat.up.sql
-- ============================================================

UPDATE `bp_menu`
SET `component` = '/payment/amount/index',
    `path` = '/payment/amounts',
    `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.title', 'system.payment.amountTitle')
WHERE `name` = 'RechargePresetList';

UPDATE `bp_menu`
SET `component` = '/payment/order/index',
    `path` = '/payment/orders',
    `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.title', 'system.payment.orderTitle')
WHERE `name` = 'PaymentOrderList';

UPDATE `bp_menu`
SET `component` = '/system/site/index',
    `path` = '/content/site',
    `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.title', 'system.site.title')
WHERE `name` = 'SiteConfig';

UPDATE `bp_menu`
SET `component` = '/friend-link/list',
    `path` = '/content/friend-links',
    `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.title', 'system.friendLink.title')
WHERE `name` = 'FriendLinkList';

UPDATE `bp_menu`
SET `component` = '/friend-link/applications',
    `path` = '/content/friend-link-applications',
    `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.title', 'system.friendLink.applicationTitle')
WHERE `name` = 'FriendLinkApplications';

UPDATE `bp_menu`
SET `component` = '/friend-link/config',
    `path` = '/content/friend-link-config',
    `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.title', 'system.friendLink.configTitle')
WHERE `name` = 'FriendLinkConfig';

UPDATE `bp_menu`
SET `component` = '/dashboard/workspace/index',
    `path` = '/dashboard-stats/admin',
    `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.title', 'system.dashboard.adminTitle')
WHERE `name` = 'AdminDashboardStats';

UPDATE `bp_menu`
SET `component` = '/dashboard/workspace/index',
    `path` = '/dashboard-stats/user',
    `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.title', 'system.dashboard.userTitle')
WHERE `name` = 'UserDashboardStats';

UPDATE `bp_menu`
SET `component` = '/interface/log',
    `path` = '/package/billing-records',
    `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.title', 'system.package.billingRecordTitle')
WHERE `name` = 'BillingRecordList';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.title', 'system.package.mallTitle')
WHERE `name` = 'PackageMall';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.title', 'system.package.userPaymentOrderTitle')
WHERE `name` = 'UserPaymentOrder';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.title', 'system.dashboard.title')
WHERE `name` = 'DashboardStats';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.title', 'system.payment.title')
WHERE `name` = 'PaymentManagement';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.title', 'system.payment.configTitle')
WHERE `name` = 'PaymentConfig';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.title', 'system.content.title')
WHERE `name` = 'ContentOperations';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.title', 'system.notice.listTitle')
WHERE `name` = 'NoticeList';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.title', 'system.notice.homeConfigTitle')
WHERE `name` = 'HomeNoticeConfig';

UPDATE `bp_menu`
SET `path` = '/dashboard/home-overview',
    `component` = '/dashboard/workspace/index',
    `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.title', 'system.home.overviewTitle')
WHERE `name` = 'HomeOverview';

UPDATE `bp_menu`
SET `component` = '/package/global/list',
    `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.title', 'system.package.globalTitle')
WHERE `name` = 'GlobalPackageList';

UPDATE `bp_menu`
SET `component` = '/package/interface/list',
    `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.title', 'system.package.interfaceTitle')
WHERE `name` = 'InterfacePackageList';

UPDATE `bp_menu`
SET `component` = '/package/point/list',
    `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.title', 'system.package.pointTitle')
WHERE `name` = 'PointPackageList';

UPDATE `bp_menu`
SET `component` = '/purchase/global/list'
WHERE `name` = 'UserGlobalPackage';

UPDATE `bp_menu`
SET `component` = '/purchase/interface/list'
WHERE `name` = 'UserInterfacePackage';

UPDATE `bp_menu`
SET `component` = '/purchase/point/list'
WHERE `name` = 'UserPointPackage';


-- ============================================================
-- Migration: 000011_console_route_namespace.up.sql
-- ============================================================

UPDATE `bp_menu`
SET `path` = '/console/workspace',
    `component` = '/dashboard/workspace/index',
    `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.title', 'page.dashboard.workspace', '$.affixTab', true, '$.keepAlive', true)
WHERE `name` = 'Workspace';

ALTER TABLE `bp_user`
  MODIFY COLUMN `home_path` varchar(128) NOT NULL DEFAULT '/console/workspace' COMMENT '登录后首页路径';

UPDATE `bp_user`
SET `home_path` = '/console/workspace'
WHERE `home_path` = '/workspace';


-- ============================================================
-- Migration: 000012_system_settings.up.sql
-- ============================================================

CREATE TABLE IF NOT EXISTS `bp_auth_qq_login_config` (
  `id` tinyint NOT NULL DEFAULT 1,
  `enabled` tinyint NOT NULL DEFAULT 0,
  `app_id` varchar(128) NOT NULL DEFAULT '',
  `app_key` varchar(255) NOT NULL DEFAULT '',
  `redirect_uri` varchar(1024) NOT NULL DEFAULT '',
  `frontend_base_url` varchar(1024) NOT NULL DEFAULT '',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  CONSTRAINT `chk_bp_auth_qq_login_config_singleton` CHECK (`id` = 1)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='QQ login reserved config';

INSERT INTO `bp_auth_qq_login_config` (`id`, `enabled`, `frontend_base_url`)
VALUES (1, 0, '')
ON DUPLICATE KEY UPDATE `id` = `id`;

INSERT INTO `bp_menu`
  (`id`, `pid`, `name`, `path`, `component`, `redirect`, `type`, `auth_code`, `meta`, `status`)
VALUES
  (150, 30, 'SystemRegisterOpenConfig', '/system/register', '/system/register/index', NULL, 'menu', 'system:register:open-config', JSON_OBJECT('title', '注册开放设置', 'icon', 'mdi:account-plus-outline', 'order', 2), 1),
  (151, 150, 'SystemRegisterOpenConfigUpdate', '', NULL, NULL, 'button', 'system:register:open-config:update', JSON_OBJECT('title', '保存注册开放设置', 'order', 1), 1),
  (152, 30, 'SystemRegisterEmailConfig', '/system/register/email', '/system/register/email-config', NULL, 'menu', 'system:register:email-config', JSON_OBJECT('title', '邮箱验证码设置', 'icon', 'mdi:email-cog-outline', 'order', 3), 1),
  (153, 152, 'SystemRegisterEmailConfigUpdate', '', NULL, NULL, 'button', 'system:register:email-config:update', JSON_OBJECT('title', '保存邮箱验证码设置', 'order', 1), 1),
  (154, 152, 'SystemRegisterEmailConfigTest', '', NULL, NULL, 'button', 'system:register:email-config:test', JSON_OBJECT('title', '测试邮箱验证码设置', 'order', 2), 1),
  (155, 30, 'SystemRegisterMobileConfig', '/system/register/mobile', '/system/register/mobile-config', NULL, 'menu', 'system:register:mobile-config', JSON_OBJECT('title', '短信验证码设置', 'icon', 'mdi:cellphone-cog', 'order', 4), 1),
  (156, 155, 'SystemRegisterMobileConfigUpdate', '', NULL, NULL, 'button', 'system:register:mobile-config:update', JSON_OBJECT('title', '保存短信验证码设置', 'order', 1), 1),
  (157, 155, 'SystemRegisterMobileConfigTest', '', NULL, NULL, 'button', 'system:register:mobile-config:test', JSON_OBJECT('title', '测试短信验证码设置', 'order', 2), 1),
  (158, 30, 'SystemQuickLoginConfig', '/system/login/quick', '/system/login/quick-login', NULL, 'menu', 'system:login:qq-config', JSON_OBJECT('title', '快捷登录设置', 'icon', 'mdi:login-variant', 'order', 5), 1),
  (159, 158, 'SystemQuickLoginConfigUpdate', '', NULL, NULL, 'button', 'system:login:qq-config:update', JSON_OBJECT('title', '保存快捷登录设置', 'order', 1), 1),
  (160, 30, 'SystemLogDelete', '/system/log-delete', '/system/log-delete/index', NULL, 'menu', 'log:call:cleanup', JSON_OBJECT('title', '日志清理设置', 'icon', 'mdi:delete-clock-outline', 'order', 6), 1)
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
SELECT 1, id FROM `bp_menu` WHERE id BETWEEN 150 AND 160;


-- ============================================================
-- Migration: 000013_risk_control_dashboard.up.sql
-- ============================================================

ALTER TABLE `bp_interface_call_log`
  ADD KEY `idx_bp_interface_call_log_client_ip_time` (`client_ip`, `created_at`),
  ADD KEY `idx_bp_interface_call_log_created_id` (`created_at`, `id`);

INSERT INTO `bp_menu`
  (`id`, `pid`, `name`, `path`, `component`, `redirect`, `type`, `auth_code`, `meta`, `status`)
VALUES
  (170, 90, 'RiskControlDashboard', '/risk-control/dashboard', '/risk-control/dashboard', NULL, 'menu', 'risk-control:view', JSON_OBJECT('title', 'system.riskControl.dashboardTitle', 'icon', 'lucide:shield-alert', 'order', 3), 1),
  (171, 90, 'RiskControlEarth', '/risk-control/earth', '/risk-control/earth', NULL, 'menu', 'risk-control:earth:view', JSON_OBJECT('title', 'system.riskControl.earthTitle', 'icon', 'lucide:globe-2', 'order', 4), 1),
  (172, 170, 'RiskControlEventList', '', NULL, NULL, 'button', 'risk-control:event:list', JSON_OBJECT('title', '风控事件列表', 'order', 1), 1),
  (173, 171, 'RiskControlEarthView', '', NULL, NULL, 'button', 'risk-control:earth:view', JSON_OBJECT('title', '地球视角监控', 'order', 1), 1)
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
SELECT 1, id FROM `bp_menu` WHERE id BETWEEN 170 AND 173;


-- ============================================================
-- Migration: 000014_menu_component_path_fix.up.sql
-- ============================================================

UPDATE `bp_menu`
SET `component` = '/payment/config/index'
WHERE `name` = 'PaymentConfig'
  AND `component` = '/payment/config';

UPDATE `bp_menu`
SET `component` = '/notice/admin/home-notice/index'
WHERE `name` = 'HomeNoticeConfig'
  AND `component` = '/notice/admin/home-notice';


-- ============================================================
-- Migration: 000015_risk_request_fingerprint.up.sql
-- ============================================================

ALTER TABLE `bp_interface_call_log`
  ADD COLUMN `request_fingerprint` char(64) NOT NULL DEFAULT '' COMMENT 'HMAC request fingerprint used for privacy-safe risk grouping' AFTER `request_value`;

UPDATE `bp_interface_call_log`
SET `request_fingerprint` = SHA2(CONCAT(
  'legacy-masked-v1:',
  COALESCE(NULLIF(`request_params`, ''), NULLIF(`request_value`, ''), '')
), 256)
WHERE `charge_type` <> 'ADMIN'
  AND `request_fingerprint` = '';


-- ============================================================
-- Migration: 000016_call_log_scope_hardening.up.sql
-- ============================================================

DELETE FROM `bp_role_menu`
WHERE `role_id` = 2
  AND `menu_id` IN (50, 93);


-- ============================================================
-- Migration: 000017_interface_scope_hardening.up.sql
-- ============================================================

DELETE FROM `bp_role_menu`
WHERE `role_id` = 2
  AND `menu_id` = 41;


-- ============================================================
-- Migration: 000018_friend_link_pending_guard.up.sql
-- ============================================================

UPDATE bp_friend_link_application current_app
JOIN bp_friend_link_application earlier_app
  ON earlier_app.id < current_app.id
 AND earlier_app.status = 'PENDING'
 AND current_app.status = 'PENDING'
 AND (earlier_app.user_id = current_app.user_id OR earlier_app.normalized_site_url = current_app.normalized_site_url)
SET current_app.status = 'REJECTED',
    current_app.reject_reason = 'Duplicate pending application migrated by system',
    current_app.review_time = NOW();

ALTER TABLE bp_friend_link_application
  ADD COLUMN pending_user_guard binary(32)
    GENERATED ALWAYS AS (CASE WHEN status = 'PENDING' THEN UNHEX(SHA2(CAST(user_id AS CHAR), 256)) ELSE NULL END) STORED,
  ADD COLUMN pending_url_guard binary(32)
    GENERATED ALWAYS AS (CASE WHEN status = 'PENDING' THEN UNHEX(SHA2(normalized_site_url, 256)) ELSE NULL END) STORED,
  ADD UNIQUE KEY uk_bp_friend_link_pending_user (pending_user_guard),
  ADD UNIQUE KEY uk_bp_friend_link_pending_url (pending_url_guard);


-- ============================================================
-- Migration: 000019_disable_unimplemented_qq_login.up.sql
-- ============================================================

UPDATE `bp_auth_qq_login_config`
SET `enabled` = 0
WHERE `id` = 1;

UPDATE `bp_menu`
SET `status` = 0
WHERE `id` IN (158, 159);


-- ============================================================
-- Migration: 000020_disable_unimplemented_sms_provider.up.sql
-- ============================================================

UPDATE `bp_auth_register_config`
SET `mobile_register_enabled` = 0
WHERE `id` = 1;

UPDATE `bp_message_provider_config`
SET `enabled` = 0
WHERE `channel` = 'sms' AND `provider_key` = 'aliyun';

UPDATE `bp_menu`
SET `status` = 0
WHERE `id` IN (155, 156, 157);


-- ============================================================
-- Migration: 000021_payment_trade_unique.up.sql
-- ============================================================

ALTER TABLE `bp_payment_order`
  ADD UNIQUE KEY `uk_bp_payment_order_trade` (`alipay_trade_no`);

ALTER TABLE `bp_payment_order`
  DROP KEY `idx_bp_payment_order_trade`;


-- ============================================================
-- Migration: 000022_user_asset_center.up.sql
-- ============================================================

INSERT INTO `bp_menu`
  (`id`, `pid`, `name`, `path`, `component`, `redirect`, `type`, `auth_code`, `meta`, `status`)
VALUES
  (174, 84, 'UserAssetCenter', '/purchase/assets', '/purchase/assets/index', NULL, 'menu', 'package:mall:list', JSON_OBJECT('title', 'system.package.assets.title', 'icon', 'lucide:wallet-cards', 'order', 5), 1)
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
SELECT id, 174 FROM `bp_role` WHERE `role_key` IN ('admin', 'user');


-- ============================================================
-- Migration: 000023_package_contract_alignment.up.sql
-- ============================================================

ALTER TABLE `bp_package_interface_spec`
  ADD COLUMN `remark` varchar(255) DEFAULT NULL COMMENT '规格备注' AFTER `status`,
  ADD COLUMN `sort_order` int NOT NULL DEFAULT 1 COMMENT '规格排序' AFTER `remark`;

ALTER TABLE `bp_package_interface_spec`
  DROP KEY `idx_bp_package_interface_spec_pkg`,
  ADD KEY `idx_bp_package_interface_spec_pkg` (`package_id`, `status`, `sort_order`, `price`, `id`);


-- ============================================================
-- Migration: 000024_package_delete_guard.up.sql
-- ============================================================

ALTER TABLE `bp_user_package_global`
  ADD KEY `idx_bp_user_package_global_package` (`package_id`, `id`);

ALTER TABLE `bp_user_package_interface`
  ADD KEY `idx_bp_user_package_interface_package` (`package_id`, `spec_id`, `id`);

ALTER TABLE `bp_payment_order`
  ADD KEY `idx_bp_payment_order_target` (`order_type`, `target_id`, `status`, `id`);


-- ============================================================
-- Migration: 000025_risk_query_covering_index.up.sql
-- ============================================================

ALTER TABLE `bp_interface_call_log`
  ADD KEY `idx_bp_call_log_risk_window` (
    `created_at`,
    `charge_type`,
    `interface_id`,
    `request_fingerprint`,
    `client_ip`,
    `user_id`,
    `api_key_id`
  );


-- ============================================================
-- Migration: 000026_call_log_size_semantics.up.sql
-- ============================================================

ALTER TABLE `bp_interface_call_log`
  MODIFY COLUMN `response_size` bigint NOT NULL DEFAULT 0 COMMENT 'original response body size bytes';


-- ============================================================
-- Migration: 000027_call_log_permission_alignment.up.sql
-- ============================================================

UPDATE `bp_menu`
SET `auth_code` = 'log:call:list'
WHERE `id` = 50 AND `name` = 'InterfaceCallLog';

INSERT IGNORE INTO `bp_role_menu` (`role_id`, `menu_id`)
SELECT `role_id`, 50
FROM `bp_role_menu`
WHERE `menu_id` = 96;

DELETE FROM `bp_role_menu` WHERE `menu_id` = 96;
DELETE FROM `bp_menu` WHERE `id` = 96 AND `name` = 'CallLogListPermission';


-- ============================================================
-- Migration: 000028_billing_reconcile_index.up.sql
-- ============================================================

ALTER TABLE `bp_billing_record`
  ADD KEY `idx_bp_billing_record_reconcile` (`status`, `created_at`, `id`);


-- ============================================================
-- Migration: 000029_wallet_balance_capacity.up.sql
-- ============================================================

ALTER TABLE `bp_user`
  MODIFY COLUMN `balance` decimal(18,4) NOT NULL DEFAULT 0.0000;

ALTER TABLE `bp_wallet_transaction`
  MODIFY COLUMN `amount` decimal(18,4) NOT NULL DEFAULT 0.0000,
  MODIFY COLUMN `balance_after` decimal(18,4) NOT NULL DEFAULT 0.0000;


-- ============================================================
-- Migration: 000030_payment_order_paid_index.up.sql
-- ============================================================

ALTER TABLE `bp_payment_order`
  ADD KEY `idx_bp_payment_order_paid` (`status`, `paid_at`);


-- ============================================================
-- Migration: 000031_balance_purchase_idempotency.up.sql
-- ============================================================

CREATE TABLE IF NOT EXISTS `bp_balance_purchase_request` (
  `id` bigint NOT NULL AUTO_INCREMENT,
  `user_id` bigint NOT NULL,
  `idempotency_key` varchar(64) NOT NULL,
  `purchase_type` varchar(16) NOT NULL COMMENT 'GLOBAL/INTERFACE/POINT',
  `target_id` bigint NOT NULL,
  `status` varchar(16) NOT NULL DEFAULT 'PROCESSING' COMMENT 'PROCESSING/COMPLETED',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `completed_at` datetime DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_bp_balance_purchase_request_user_key` (`user_id`, `idempotency_key`),
  KEY `idx_bp_balance_purchase_request_created` (`created_at`, `id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='Balance purchase idempotency ledger';


-- ============================================================
-- Migration: 000032_free_package_claim_guard.up.sql
-- ============================================================

ALTER TABLE `bp_wallet_transaction`
  ADD KEY `idx_bp_wallet_transaction_user_biz` (`user_id`, `biz_type`, `biz_id`);


-- ============================================================
-- Migration: 000033_call_log_truncation_flag.up.sql
-- ============================================================

ALTER TABLE `bp_interface_call_log`
  ADD COLUMN `response_truncated` tinyint NOT NULL DEFAULT 0 COMMENT 'response body was truncated or omitted before persistence' AFTER `response_size`;

UPDATE `bp_interface_call_log`
SET `response_truncated` = 1
WHERE `response_body` = '[BINARY_RESPONSE_OMITTED]'
   OR (`response_size` > 0 AND `response_size` > OCTET_LENGTH(COALESCE(`response_body`, '')));


-- ============================================================
-- Migration: 000034_call_log_saved_size.up.sql
-- ============================================================

ALTER TABLE `bp_interface_call_log`
  ADD COLUMN `response_saved_size` bigint NOT NULL DEFAULT 0 COMMENT 'persisted response body size after masking and truncation' AFTER `response_size`;

UPDATE `bp_interface_call_log`
SET `response_saved_size` = OCTET_LENGTH(COALESCE(`response_body`, ''));


-- ============================================================
-- Migration: 000035_payment_notify_replay_guard.up.sql
-- ============================================================

ALTER TABLE `bp_payment_notify_log`
  ADD COLUMN `attempt_count` int unsigned NOT NULL DEFAULT 1 COMMENT 'number of deliveries observed for this notification' AFTER `payload`,
  ADD COLUMN `last_seen_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT 'last delivery time' AFTER `created_at`;

UPDATE `bp_payment_notify_log` target
INNER JOIN (
  SELECT event_type, notify_id, COUNT(*) attempts, MAX(created_at) last_seen
  FROM `bp_payment_notify_log`
  WHERE notify_id IS NOT NULL AND notify_id <> ''
  GROUP BY event_type, notify_id
) aggregate_rows
  ON aggregate_rows.event_type = target.event_type
 AND aggregate_rows.notify_id = target.notify_id
SET target.attempt_count = aggregate_rows.attempts,
    target.last_seen_at = aggregate_rows.last_seen;

DELETE loser
FROM `bp_payment_notify_log` loser
INNER JOIN `bp_payment_notify_log` winner
  ON winner.event_type = loser.event_type
 AND winner.notify_id = loser.notify_id
 AND (
      winner.processed > loser.processed
      OR (winner.processed = loser.processed AND winner.verified > loser.verified)
      OR (winner.processed = loser.processed AND winner.verified = loser.verified AND winner.id < loser.id)
 )
WHERE loser.notify_id IS NOT NULL
  AND loser.notify_id <> '';

ALTER TABLE `bp_payment_notify_log`
  ADD UNIQUE KEY `uk_bp_payment_notify_event_id` (`event_type`, `notify_id`);


-- ============================================================
-- Migration: 000036_payment_wechat.up.sql
-- ============================================================

CREATE TABLE IF NOT EXISTS `bp_payment_wechat_config` (
  `id` bigint NOT NULL DEFAULT 1 COMMENT 'singleton id',
  `enabled` tinyint NOT NULL DEFAULT 0,
  `mode` varchar(16) NOT NULL DEFAULT 'SANDBOX' COMMENT 'SANDBOX/PRODUCTION',
  `mchid` varchar(64) NOT NULL DEFAULT '',
  `app_id` varchar(64) NOT NULL DEFAULT '',
  `apiv3_key_cipher` text,
  `private_key_cipher` text,
  `serial_no` varchar(64) DEFAULT NULL COMMENT '商户API证书序列号',
  `platform_public_key_id` varchar(64) DEFAULT NULL,
  `platform_public_key_cipher` text,
  `notify_url` varchar(512) DEFAULT NULL,
  `sp_mchid` varchar(64) DEFAULT NULL,
  `sp_app_id` varchar(64) DEFAULT NULL,
  `sub_mchid` varchar(64) DEFAULT NULL,
  `sub_app_id` varchar(64) DEFAULT NULL,
  `oa_app_id` varchar(64) DEFAULT NULL COMMENT '公众号 appid(JSAPI)',
  `mini_app_id` varchar(64) DEFAULT NULL COMMENT '小程序 appid',
  `app_app_id` varchar(64) DEFAULT NULL COMMENT 'APP appid',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='WeChat Pay v3 config';

INSERT INTO `bp_payment_wechat_config` (`id`, `mode`, `enabled`)
VALUES (1, 'SANDBOX', 0)
ON DUPLICATE KEY UPDATE `id` = `id`;

ALTER TABLE `bp_payment_order`
  ADD COLUMN `trade_type` varchar(16) DEFAULT NULL COMMENT 'WECHAT: NATIVE/H5/JSAPI/APP/MINI; 支付宝留空' AFTER `product`,
  ADD COLUMN `prepay_id` varchar(64) DEFAULT NULL COMMENT '微信预支付ID' AFTER `qr_code`,
  ADD COLUMN `channel_trade_no` varchar(128) DEFAULT NULL COMMENT '通道交易号(微信transaction_id)' AFTER `alipay_trade_no`;

-- 微信配置权限按钮（复用支付配置菜单 101 作为父级，auth_code 独立便于灰度）。
INSERT INTO `bp_menu`
  (`id`, `pid`, `name`, `path`, `component`, `redirect`, `type`, `auth_code`, `meta`, `status`)
VALUES
  (111, 101, 'WechatConfigUpdate', '', NULL, NULL, 'button', 'payment:wechat:update', JSON_OBJECT('title', 'Update WeChat Config'), 1)
ON DUPLICATE KEY UPDATE `pid` = VALUES(`pid`), `auth_code` = VALUES(`auth_code`), `meta` = VALUES(`meta`), `status` = VALUES(`status`);

INSERT IGNORE INTO `bp_role_menu` (`role_id`, `menu_id`) SELECT 1, 111;


-- ============================================================
-- Migration: 000037_payment_wechat_cert.up.sql
-- ============================================================

CREATE TABLE IF NOT EXISTS `bp_payment_wechat_platform_cert` (
  `id` bigint NOT NULL AUTO_INCREMENT,
  `serial_no` varchar(64) NOT NULL,
  `public_key_cipher` text,
  `effective_time` datetime DEFAULT NULL,
  `expire_time` datetime DEFAULT NULL,
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_wechat_cert_serial` (`serial_no`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='WeChat platform cert cache';


-- ============================================================
-- Migration: 000038_ip_config.up.sql
-- ============================================================

-- IP 归属地数据源后台配置（单点表，id=1）
-- data_source_name: default=内置 ip-api.com；custom_xxx=自定义 URL + 字段映射
CREATE TABLE IF NOT EXISTS `bp_ip_config` (
  `id` bigint NOT NULL DEFAULT 1,
  `data_source_name` varchar(64) NOT NULL DEFAULT 'default',
  `api_url` varchar(1024) NOT NULL DEFAULT '',
  `parse_rule` varchar(512) NOT NULL DEFAULT 'country=$.country, province=$.regionName, city=$.city, isp=$.isp',
  `enabled` tinyint NOT NULL DEFAULT 1,
  `remark` varchar(255) NOT NULL DEFAULT '',
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='IP region data source config singleton';

INSERT INTO `bp_ip_config` (`id`, `data_source_name`, `api_url`, `parse_rule`, `enabled`, `remark`)
VALUES (1, 'default', '', 'country=$.country, province=$.regionName, city=$.city, isp=$.isp', 1, '内置默认源：ip-api.com（http://ip-api.com/json/{ip}?lang=zh-CN）')
ON DUPLICATE KEY UPDATE `id` = `id`;


-- ============================================================
-- Migration: 000039_seo_settings.up.sql
-- ============================================================

-- SEO 后台设置：bp_site_config 新增 SEO 相关列（P0 4 字段 + noindex 收录开关）
-- seo_title:     SEO 标题（空时前端回退 site_name）
-- seo_keywords:  SEO 关键词（英文逗号分隔，保存时中文逗号规范化为英文逗号）
-- og_image:      OG 分享图 URL（https?:// 绝对地址或 /system-api/upload/ 相对路径，可空）
-- site_url:      站点对外基础地址（https?:// 开头，结尾不带 /，可空；用于 sitemap/canonical）
-- noindex:       收录开关（0=允许收录，1=禁止收录，页面输出 noindex 且 sitemap 停发）
ALTER TABLE `bp_site_config`
  ADD COLUMN `seo_title` varchar(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT '' COMMENT 'SEO 标题' AFTER `copyright`,
  ADD COLUMN `seo_keywords` varchar(512) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT '' COMMENT 'SEO 关键词（英文逗号分隔）' AFTER `seo_title`,
  ADD COLUMN `og_image` varchar(1024) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT '' COMMENT 'OG 分享图 URL' AFTER `seo_keywords`,
  ADD COLUMN `site_url` varchar(255) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT '' COMMENT '站点对外基础地址（sitemap/canonical）' AFTER `og_image`,
  ADD COLUMN `noindex` tinyint(1) NOT NULL DEFAULT 0 COMMENT '收录开关：1=禁止收录' AFTER `site_url`;


-- ============================================================
-- Migration: 000040_call_log_request_value_fix.up.sql
-- ============================================================

-- 修复历史缺陷：调用日志“链接或值”(request_value) 列曾误写入客户端 IP，
-- 导致该列与“调用IP”(client_ip) 列显示相同值（错位/重复）。
-- 只清理 request_value 精确等于 client_ip、且 request_value 本身是合法 IP 字面量的行，
-- 避免误伤正常把 IP 作为请求值传入的业务数据。
UPDATE `bp_interface_call_log`
SET `request_value` = ''
WHERE `request_value` = `client_ip`
  AND (`request_value` REGEXP '^[0-9]{1,3}(\\.[0-9]{1,3}){3}$' OR INET6_ATON(`request_value`) IS NOT NULL)
  AND `request_value` <> '';


-- ============================================================
-- Migration: 000041_ip_config_menu.up.sql
-- ============================================================

-- 后台「系统设置」菜单补充 IP 数据源配置入口（对应 /system/ip-config 前端页面）。
-- 前端路由已静态注册（router/routes/modules/system.ts），这里补 DB 菜单，让侧边栏可见。
INSERT INTO `bp_menu`
  (`id`, `pid`, `name`, `path`, `component`, `redirect`, `type`, `auth_code`, `meta`, `status`)
VALUES
  (176, 30, 'SystemIpSourceConfig', '/system/ip-config', '/system/ip-config/index', NULL, 'menu', 'system:ip-config', JSON_OBJECT('title', 'IP 数据源', 'icon', 'mdi:ip-network', 'order', 7), 1),
  (177, 176, 'SystemIpSourceConfigUpdate', '', NULL, NULL, 'button', 'system:ip-config:update', JSON_OBJECT('title', '保存IP数据源配置', 'order', 1), 1)
ON DUPLICATE KEY UPDATE
  `pid` = VALUES(`pid`),
  `path` = VALUES(`path`),
  `component` = VALUES(`component`),
  `redirect` = VALUES(`redirect`),
  `type` = VALUES(`type`),
  `auth_code` = VALUES(`auth_code`),
  `meta` = VALUES(`meta`),
  `status` = VALUES(`status`);

-- 授权给管理员角色（role_id=1），与其它菜单种子保持一致。
INSERT IGNORE INTO `bp_role_menu` (`role_id`, `menu_id`)
SELECT 1, id FROM `bp_menu` WHERE id BETWEEN 176 AND 177;


-- ============================================================
-- Migration: 000042_about_menu.up.sql
-- ============================================================

-- 后台「系统设置」菜单补充「关于系统」入口（对应 /system/about 前端页面）。
-- 前端路由已静态注册（router/routes/modules/system.ts），这里补 DB 菜单，让侧边栏可见。
INSERT INTO `bp_menu`
  (`id`, `pid`, `name`, `path`, `component`, `redirect`, `type`, `auth_code`, `meta`, `status`)
VALUES
  (178, 0, 'SystemAbout', '/system/about', '/system/about/index', NULL, 'menu', 'system:about:view', JSON_OBJECT('title', 'system.about.title', 'icon', 'mdi:information-outline', 'order', 9999), 1)
ON DUPLICATE KEY UPDATE
  `pid` = VALUES(`pid`),
  `path` = VALUES(`path`),
  `component` = VALUES(`component`),
  `redirect` = VALUES(`redirect`),
  `type` = VALUES(`type`),
  `auth_code` = VALUES(`auth_code`),
  `meta` = VALUES(`meta`),
  `status` = VALUES(`status`);

-- 授权给管理员角色（role_id=1），与其它菜单种子保持一致。
INSERT IGNORE INTO `bp_role_menu` (`role_id`, `menu_id`)
SELECT 1, id FROM `bp_menu` WHERE id = 178;


-- ============================================================
-- Migration: 000043_captcha_config_menu.up.sql
-- ============================================================

-- 后台「系统设置」菜单补充「验证码配置」入口（对应 /system/captcha-config 前端页面）。
-- 前端路由已静态注册（router/routes/modules/system.ts），这里补 DB 菜单，让侧边栏可见。
INSERT INTO `bp_menu`
  (`id`, `pid`, `name`, `path`, `component`, `redirect`, `type`, `auth_code`, `meta`, `status`)
VALUES
  (179, 30, 'SystemCaptchaConfig', '/system/captcha-config', '/system/captcha-config/index', NULL, 'menu', 'system:captcha-config:view', JSON_OBJECT('title', '验证码配置', 'icon', 'mdi:shield-key-outline', 'order', 9), 1),
  (180, 179, 'SystemCaptchaConfigUpdate', '', NULL, NULL, 'button', 'system:captcha-config:update', JSON_OBJECT('title', '保存验证码配置', 'order', 1), 1)
ON DUPLICATE KEY UPDATE
  `pid` = VALUES(`pid`),
  `path` = VALUES(`path`),
  `component` = VALUES(`component`),
  `redirect` = VALUES(`redirect`),
  `type` = VALUES(`type`),
  `auth_code` = VALUES(`auth_code`),
  `meta` = VALUES(`meta`),
  `status` = VALUES(`status`);

-- 授权给管理员角色（role_id=1），与其它菜单种子保持一致。
INSERT IGNORE INTO `bp_role_menu` (`role_id`, `menu_id`)
SELECT 1, id FROM `bp_menu` WHERE id BETWEEN 179 AND 180;


-- ============================================================
-- Migration: 000044_ip_region_https_coordinates.up.sql
-- ============================================================

-- Upgrade the built-in IP location defaults to an HTTPS source that returns
-- coordinates. Only untouched default rows are changed; custom providers and
-- administrator-edited mappings remain intact.
ALTER TABLE `bp_ip_config`
  MODIFY COLUMN `parse_rule` varchar(512) NOT NULL DEFAULT 'country=$.country, province=$.region, city=$.city, isp=$.connection.isp, latitude=$.latitude, longitude=$.longitude';

UPDATE `bp_ip_config`
SET `parse_rule` = 'country=$.country, province=$.region, city=$.city, isp=$.connection.isp, latitude=$.latitude, longitude=$.longitude',
    `remark` = '内置默认源：ipwho.is（HTTPS，含经纬度）；失败时自动回退国家级 HTTPS 数据源'
WHERE `id` = 1
  AND LOWER(TRIM(`data_source_name`)) = 'default'
  AND TRIM(`api_url`) = ''
  AND TRIM(`parse_rule`) = 'country=$.country, province=$.regionName, city=$.city, isp=$.isp'
  AND (
    TRIM(COALESCE(`remark`, '')) = ''
    OR TRIM(`remark`) = '内置默认源：ip-api.com（http://ip-api.com/json/{ip}?lang=zh-CN）'
  );


-- ============================================================
-- Migration: 000045_captcha_provider_defaults.up.sql
-- ============================================================

-- Ensure every concrete third-party captcha implementation is represented by
-- a safe, disabled default row. Existing credentials and administrator choices
-- are never overwritten.
INSERT INTO `bp_captcha_provider_config`
  (`provider_key`, `scene`, `enabled`, `timeout_ms`, `failure_policy`, `extra_config`)
VALUES
  ('geetest4', 'default', 0, 3000, 'deny', JSON_OBJECT('product', 'float')),
  ('cloudflare', 'default', 0, 3000, 'deny', JSON_OBJECT())
ON DUPLICATE KEY UPDATE
  `provider_key` = VALUES(`provider_key`);

-- The runtime only supports fail-closed verification. Normalize legacy rows so
-- database state cannot silently request an unsupported fail-open policy.
UPDATE `bp_captcha_provider_config`
SET `failure_policy` = 'deny'
WHERE `failure_policy` IS NULL OR LOWER(TRIM(`failure_policy`)) <> 'deny';


-- ============================================================
-- Migration: 000046_advertising_custom_code.up.sql
-- ============================================================

-- 广告投放与站点自定义代码配置。
-- 广告使用结构化内容，前端不渲染后台提交的 HTML，避免广告内容直接形成 XSS。
CREATE TABLE `bp_advertisement` (
  `id` bigint NOT NULL AUTO_INCREMENT COMMENT '广告编号',
  `name` varchar(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '后台名称',
  `placement` varchar(32) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '投放位：HOME/CONSOLE/NOTICE/SERVICE_PROVIDER/API_RESPONSE',
  `title` varchar(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '展示标题',
  `description` varchar(512) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT '' COMMENT '展示说明',
  `image_url` varchar(1024) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT '' COMMENT '图片地址',
  `link_url` varchar(1024) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT '' COMMENT '跳转地址',
  `link_target` varchar(16) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT '_blank' COMMENT '跳转方式',
  `payload_json` json NULL COMMENT '接口广告扩展 JSON',
  `status` tinyint NOT NULL DEFAULT 1 COMMENT '状态：1启用，0停用',
  `sort_no` int NOT NULL DEFAULT 0 COMMENT '排序值，越大越优先',
  `start_at` datetime NULL DEFAULT NULL COMMENT '开始展示时间',
  `end_at` datetime NULL DEFAULT NULL COMMENT '结束展示时间',
  `created_by` bigint NULL DEFAULT NULL COMMENT '创建人',
  `updated_by` bigint NULL DEFAULT NULL COMMENT '更新人',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`) USING BTREE,
  INDEX `idx_bp_ad_placement_active` (`placement`, `status`, `start_at`, `end_at`, `sort_no`) USING BTREE,
  INDEX `idx_bp_ad_updated_by` (`updated_by`) USING BTREE
) ENGINE=InnoDB CHARACTER SET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='结构化广告投放配置' ROW_FORMAT=Dynamic;

CREATE TABLE `bp_custom_code_config` (
  `id` bigint NOT NULL DEFAULT 1 COMMENT '固定配置编号',
  `enabled` tinyint NOT NULL DEFAULT 0 COMMENT '是否启用',
  `css_code` mediumtext CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '站点自定义 CSS',
  `js_code` mediumtext CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci NOT NULL COMMENT '站点自定义 JavaScript',
  `updated_by` bigint NULL DEFAULT NULL COMMENT '更新人',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`) USING BTREE
) ENGINE=InnoDB CHARACTER SET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='站点自定义 CSS/JS 配置' ROW_FORMAT=Dynamic;

INSERT INTO `bp_custom_code_config` (`id`, `enabled`, `css_code`, `js_code`)
VALUES (1, 0, '', '')
ON DUPLICATE KEY UPDATE `id`=VALUES(`id`);

INSERT INTO `bp_menu`
  (`id`,`pid`,`name`,`path`,`component`,`redirect`,`type`,`auth_code`,`meta`,`status`)
VALUES
  (181,120,'AdvertisementList','/content/advertisements','/advertisement/list',NULL,'menu','advertisement:list',JSON_OBJECT('title','广告投放','icon','mdi:advertisements','order',7),1),
  (182,181,'AdvertisementCreate','',NULL,NULL,'button','advertisement:create',JSON_OBJECT('title','新增广告','order',1),1),
  (183,181,'AdvertisementUpdate','',NULL,NULL,'button','advertisement:update',JSON_OBJECT('title','编辑广告','order',2),1),
  (184,181,'AdvertisementDelete','',NULL,NULL,'button','advertisement:delete',JSON_OBJECT('title','删除广告','order',3),1),
  (185,30,'SystemCustomCode','/system/custom-code','/system/custom-code/index',NULL,'menu','system:custom-code:view',JSON_OBJECT('title','自定义代码','icon','mdi:code-braces-box','order',10),1),
  (186,185,'SystemCustomCodeUpdate','',NULL,NULL,'button','system:custom-code:update',JSON_OBJECT('title','保存自定义代码','order',1),1)
ON DUPLICATE KEY UPDATE
  `pid`=VALUES(`pid`),`path`=VALUES(`path`),`component`=VALUES(`component`),
  `redirect`=VALUES(`redirect`),`type`=VALUES(`type`),`auth_code`=VALUES(`auth_code`),
  `meta`=VALUES(`meta`),`status`=VALUES(`status`);

INSERT IGNORE INTO `bp_role_menu` (`role_id`,`menu_id`)
SELECT 1,`id` FROM `bp_menu` WHERE `id` BETWEEN 181 AND 186;


-- ============================================================
-- Migration: 000047_interface_delivery_policy.up.sql
-- ============================================================

-- 接口开放网关的响应缓存与 JSON 广告注入策略。
ALTER TABLE `bp_interface_api`
  ADD COLUMN `cache_enabled` tinyint NOT NULL DEFAULT 0 COMMENT '是否启用开放网关响应缓存' AFTER `polling_check_expected`,
  ADD COLUMN `cache_default_ttl_seconds` int NOT NULL DEFAULT 0 COMMENT '未命中条件规则时的默认缓存秒数' AFTER `cache_enabled`,
  ADD COLUMN `cache_rules` json NULL COMMENT '按响应状态与 JSON 字段匹配的缓存规则' AFTER `cache_default_ttl_seconds`,
  ADD COLUMN `response_ad_enabled` tinyint NOT NULL DEFAULT 0 COMMENT '是否向 JSON 对象响应注入广告字段' AFTER `cache_rules`,
  ADD COLUMN `response_ad_id` bigint NULL DEFAULT NULL COMMENT '指定接口广告；为空时自动选择当前有效广告' AFTER `response_ad_enabled`,
  ADD COLUMN `response_ad_field` varchar(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT '_advertisement' COMMENT '广告 JSON 字段名' AFTER `response_ad_id`,
  ADD INDEX `idx_bp_interface_cache_enabled` (`cache_enabled`),
  ADD INDEX `idx_bp_interface_response_ad` (`response_ad_enabled`,`response_ad_id`),
  ADD CONSTRAINT `fk_bp_interface_response_ad` FOREIGN KEY (`response_ad_id`) REFERENCES `bp_advertisement` (`id`) ON DELETE SET NULL ON UPDATE RESTRICT;

INSERT INTO `bp_menu`
  (`id`,`pid`,`name`,`path`,`component`,`redirect`,`type`,`auth_code`,`meta`,`status`)
VALUES
  (187,41,'InterfaceDeliveryPolicy','',NULL,NULL,'button','interface:delivery-policy',JSON_OBJECT('title','投放与缓存策略','order',9),1)
ON DUPLICATE KEY UPDATE
  `pid`=VALUES(`pid`),`type`=VALUES(`type`),`auth_code`=VALUES(`auth_code`),
  `meta`=VALUES(`meta`),`status`=VALUES(`status`);

INSERT IGNORE INTO `bp_role_menu` (`role_id`,`menu_id`) VALUES (1,187);


-- ============================================================
-- Migration: 000048_api_key_recoverable_secret.up.sql
-- ============================================================

SET @bugpk_schema = DATABASE();
SET @bugpk_sql = IF(EXISTS(
  SELECT 1
  FROM information_schema.COLUMNS
  WHERE TABLE_SCHEMA = @bugpk_schema
    AND TABLE_NAME = 'bp_api_key'
    AND COLUMN_NAME = 'secret_ciphertext'
),
  'SELECT 1 WHERE 0',
  'ALTER TABLE `bp_api_key` ADD COLUMN `secret_ciphertext` varchar(512) DEFAULT NULL COMMENT ''API Key可恢复密文（AES-GCM）'' AFTER `key_hash`'
);
PREPARE bugpk_stmt FROM @bugpk_sql;
EXECUTE bugpk_stmt;
DEALLOCATE PREPARE bugpk_stmt;


-- ============================================================
-- Migration: 000049_global_interface_delivery_policy.up.sql
-- ============================================================

-- Global interface delivery policy with independent per-interface overrides.
-- Effective order: interface override > global policy > system defaults.
--
-- Compatibility repair: an earlier package could mark schema version 48 after
-- applying this policy while still missing the recoverable API-key column.
-- Keep the repair here as well so upgrading such a database converges safely.

SET @bugpk_schema = DATABASE();

SET @bugpk_sql = IF(EXISTS(
  SELECT 1 FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_schema
    AND TABLE_NAME = 'bp_api_key'
    AND COLUMN_NAME = 'secret_ciphertext'
), 'SELECT 1 WHERE 0',
  'ALTER TABLE `bp_api_key` ADD COLUMN `secret_ciphertext` varchar(512) DEFAULT NULL COMMENT ''API Key可恢复密文（AES-GCM）'' AFTER `key_hash`');
PREPARE bugpk_stmt FROM @bugpk_sql;
EXECUTE bugpk_stmt;
DEALLOCATE PREPARE bugpk_stmt;

SET @bugpk_add_cache_override = IF(EXISTS(
  SELECT 1 FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_schema
    AND TABLE_NAME = 'bp_interface_api'
    AND COLUMN_NAME = 'cache_policy_override'
), 0, 1);
SET @bugpk_sql = IF(@bugpk_add_cache_override = 1,
  'ALTER TABLE `bp_interface_api` ADD COLUMN `cache_policy_override` tinyint NOT NULL DEFAULT 0 COMMENT ''1=use interface cache policy, 0=inherit global'' AFTER `polling_check_expected`',
  'SELECT 1 WHERE 0');
PREPARE bugpk_stmt FROM @bugpk_sql;
EXECUTE bugpk_stmt;
DEALLOCATE PREPARE bugpk_stmt;

SET @bugpk_add_ad_override = IF(EXISTS(
  SELECT 1 FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_schema
    AND TABLE_NAME = 'bp_interface_api'
    AND COLUMN_NAME = 'response_ad_policy_override'
), 0, 1);
SET @bugpk_sql = IF(@bugpk_add_ad_override = 1,
  'ALTER TABLE `bp_interface_api` ADD COLUMN `response_ad_policy_override` tinyint NOT NULL DEFAULT 0 COMMENT ''1=use interface advertisement policy, 0=inherit global'' AFTER `response_ad_field`',
  'SELECT 1 WHERE 0');
PREPARE bugpk_stmt FROM @bugpk_sql;
EXECUTE bugpk_stmt;
DEALLOCATE PREPARE bugpk_stmt;

SET @bugpk_sql = IF(EXISTS(
  SELECT 1 FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_schema
    AND TABLE_NAME = 'bp_interface_api'
    AND COLUMN_NAME = 'delivery_policy_version'
), 'SELECT 1 WHERE 0',
  'ALTER TABLE `bp_interface_api` ADD COLUMN `delivery_policy_version` bigint NOT NULL DEFAULT 1 COMMENT ''Version used to invalidate response-cache keys'' AFTER `response_ad_policy_override`');
PREPARE bugpk_stmt FROM @bugpk_sql;
EXECUTE bugpk_stmt;
DEALLOCATE PREPARE bugpk_stmt;

SET @bugpk_sql = IF(EXISTS(
  SELECT 1 FROM information_schema.STATISTICS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_schema
    AND TABLE_NAME = 'bp_interface_api'
    AND INDEX_NAME = 'idx_bp_interface_cache_override'
), 'SELECT 1 WHERE 0',
  'ALTER TABLE `bp_interface_api` ADD INDEX `idx_bp_interface_cache_override` (`cache_policy_override`)');
PREPARE bugpk_stmt FROM @bugpk_sql;
EXECUTE bugpk_stmt;
DEALLOCATE PREPARE bugpk_stmt;

SET @bugpk_sql = IF(EXISTS(
  SELECT 1 FROM information_schema.STATISTICS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_schema
    AND TABLE_NAME = 'bp_interface_api'
    AND INDEX_NAME = 'idx_bp_interface_ad_override'
), 'SELECT 1 WHERE 0',
  'ALTER TABLE `bp_interface_api` ADD INDEX `idx_bp_interface_ad_override` (`response_ad_policy_override`)');
PREPARE bugpk_stmt FROM @bugpk_sql;
EXECUTE bugpk_stmt;
DEALLOCATE PREPARE bugpk_stmt;

-- Existing rows previously had only interface-local settings. Backfill only
-- columns introduced by this run so reruns never reset an administrator's
-- deliberate choice to inherit the global policy.
SET @bugpk_sql = IF(@bugpk_add_cache_override = 1,
  'UPDATE `bp_interface_api` SET `cache_policy_override` = 1 WHERE `id` > 0',
  'SELECT 1 WHERE 0');
PREPARE bugpk_stmt FROM @bugpk_sql;
EXECUTE bugpk_stmt;
DEALLOCATE PREPARE bugpk_stmt;

SET @bugpk_sql = IF(@bugpk_add_ad_override = 1,
  'UPDATE `bp_interface_api` SET `response_ad_policy_override` = 1 WHERE `id` > 0',
  'SELECT 1 WHERE 0');
PREPARE bugpk_stmt FROM @bugpk_sql;
EXECUTE bugpk_stmt;
DEALLOCATE PREPARE bugpk_stmt;

CREATE TABLE IF NOT EXISTS `bp_interface_global_delivery_policy` (
  `id` bigint NOT NULL DEFAULT 1 COMMENT 'Singleton id; always 1',
  `cache_enabled` tinyint NOT NULL DEFAULT 0 COMMENT 'Whether the response cache is enabled',
  `cache_default_ttl_seconds` int NOT NULL DEFAULT 0 COMMENT 'Default cache TTL when no rule matches',
  `cache_rules` json NULL COMMENT 'Conditional response-cache rules',
  `response_ad_enabled` tinyint NOT NULL DEFAULT 0 COMMENT 'Whether to inject an advertisement field',
  `response_ad_id` bigint NULL DEFAULT NULL COMMENT 'Selected API response advertisement; null means automatic',
  `response_ad_field` varchar(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT '_advertisement' COMMENT 'Injected JSON field name',
  `policy_version` bigint NOT NULL DEFAULT 1 COMMENT 'Version used to invalidate response-cache keys',
  `updated_by` bigint NULL DEFAULT NULL,
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`) USING BTREE,
  INDEX `idx_bp_global_delivery_cache` (`cache_enabled`),
  INDEX `idx_bp_global_delivery_ad` (`response_ad_enabled`, `response_ad_id`),
  CONSTRAINT `fk_bp_global_delivery_response_ad` FOREIGN KEY (`response_ad_id`) REFERENCES `bp_advertisement` (`id`) ON DELETE SET NULL ON UPDATE RESTRICT
) ENGINE=InnoDB CHARACTER SET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='Global interface delivery and cache policy' ROW_FORMAT=Dynamic;

SET @bugpk_sql = IF(EXISTS(
  SELECT 1 FROM information_schema.REFERENTIAL_CONSTRAINTS
  WHERE BINARY CONSTRAINT_SCHEMA = BINARY @bugpk_schema
    AND TABLE_NAME = 'bp_interface_global_delivery_policy'
    AND CONSTRAINT_NAME = 'fk_bp_global_delivery_response_ad'
), 'SELECT 1 WHERE 0',
  'ALTER TABLE `bp_interface_global_delivery_policy` ADD CONSTRAINT `fk_bp_global_delivery_response_ad` FOREIGN KEY (`response_ad_id`) REFERENCES `bp_advertisement` (`id`) ON DELETE SET NULL ON UPDATE RESTRICT');
PREPARE bugpk_stmt FROM @bugpk_sql;
EXECUTE bugpk_stmt;
DEALLOCATE PREPARE bugpk_stmt;

INSERT INTO `bp_interface_global_delivery_policy`
  (`id`, `cache_enabled`, `cache_default_ttl_seconds`, `cache_rules`, `response_ad_enabled`, `response_ad_id`, `response_ad_field`, `policy_version`)
VALUES (1, 0, 0, NULL, 0, NULL, '_advertisement', 1)
ON DUPLICATE KEY UPDATE `id` = VALUES(`id`);


-- ============================================================
-- Migration: 000050_dedupe_user_workspace_menu.up.sql
-- ============================================================

-- Keep the user-facing workspace as the single workspace entry point.
-- UserDashboardStats renders the same /dashboard/workspace/index component
-- as Workspace, so disable the duplicate menu while retaining its row and
-- role bindings for backwards-compatible rollback.
UPDATE `bp_menu`
SET `status` = 0
WHERE `name` = 'UserDashboardStats'
  AND `path` = '/dashboard-stats/user';


-- ============================================================
-- Migration: 000051_interface_request_validation.up.sql
-- ============================================================

-- Per-interface request parameter validation policy for the open gateway.
-- Rules are evaluated before billing, response caching, and upstream calls.
ALTER TABLE `bp_interface_api`
  ADD COLUMN `request_validation_enabled` tinyint NOT NULL DEFAULT 0 COMMENT 'Whether open-gateway request validation is enabled' AFTER `delivery_policy_version`,
  ADD COLUMN `request_validation_default_result` varchar(32) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'PASS' COMMENT 'PASS/INVALID_PARAMS/PLATFORM_MISMATCH when no rule matches' AFTER `request_validation_enabled`,
  ADD COLUMN `request_validation_rules` json NULL COMMENT 'Ordered request validation rules with ALL/ANY condition logic' AFTER `request_validation_default_result`,
  ADD INDEX `idx_bp_interface_request_validation` (`request_validation_enabled`);

INSERT INTO `bp_menu`
  (`id`,`pid`,`name`,`path`,`component`,`redirect`,`type`,`auth_code`,`meta`,`status`)
VALUES
  (188,41,'InterfaceRequestValidation','',NULL,NULL,'button','interface:request-validation',JSON_OBJECT('title','请求参数检测','order',10),1)
ON DUPLICATE KEY UPDATE
  `pid`=VALUES(`pid`),`type`=VALUES(`type`),`auth_code`=VALUES(`auth_code`),
  `meta`=VALUES(`meta`),`status`=VALUES(`status`);

INSERT IGNORE INTO `bp_role_menu` (`role_id`,`menu_id`) VALUES (1,188);


-- ============================================================
-- Migration: 000052_interface_response_cleaning.up.sql
-- ============================================================

-- Optional per-interface normalization of short-video upstream JSON responses.
-- Disabled by default so existing interfaces continue to pass through their
-- upstream response unchanged.
ALTER TABLE `bp_interface_api`
  ADD COLUMN `response_cleaning_enabled` tinyint NOT NULL DEFAULT 0
    COMMENT 'Whether short-video upstream JSON response cleaning is enabled'
    AFTER `polling_check_expected`;


-- ============================================================
-- Migration: 000053_site_loading_animation.up.sql
-- ============================================================

-- Optional custom startup animation. The application always keeps its built-in
-- lightweight CSS loader as a safe fallback when this option is disabled.
ALTER TABLE `bp_site_config`
  ADD COLUMN `loading_animation_enabled` tinyint NOT NULL DEFAULT 0
    COMMENT '1=use configured startup animation image, 0=use built-in CSS animation'
    AFTER `noindex`,
  ADD COLUMN `loading_animation_url` varchar(1024) NOT NULL DEFAULT ''
    COMMENT 'Uploaded or external startup animation image URL'
    AFTER `loading_animation_enabled`;

INSERT INTO `bp_menu`
  (`id`,`pid`,`name`,`path`,`component`,`redirect`,`type`,`auth_code`,`meta`,`status`)
VALUES
  (189,130,'SiteLoadingAnimationUpload','',NULL,NULL,'button','site:upload-loading-animation',JSON_OBJECT('title','上传启动动画','order',2),1)
ON DUPLICATE KEY UPDATE
  `pid`=VALUES(`pid`),`type`=VALUES(`type`),`auth_code`=VALUES(`auth_code`),
  `meta`=VALUES(`meta`),`status`=VALUES(`status`);

INSERT IGNORE INTO `bp_role_menu` (`role_id`,`menu_id`) VALUES (1,189);


-- ============================================================
-- Migration: 000054_donation_settings.up.sql
-- ============================================================

CREATE TABLE IF NOT EXISTS `bp_donation_config` (
  `id` bigint NOT NULL DEFAULT 1 COMMENT 'Singleton id; always 1',
  `enabled` tinyint NOT NULL DEFAULT 0 COMMENT 'Whether the public donation page is published',
  `eyebrow` varchar(64) NOT NULL DEFAULT '赞赏',
  `title` varchar(120) NOT NULL DEFAULT '赞赏支持',
  `description` varchar(500) NOT NULL DEFAULT '',
  `scan_title` varchar(120) NOT NULL DEFAULT '扫码赞赏',
  `scan_description` varchar(255) NOT NULL DEFAULT '',
  `thanks` varchar(255) NOT NULL DEFAULT '',
  `methods` json NOT NULL COMMENT 'Ordered donation methods and QR-code settings',
  `updated_by` bigint DEFAULT NULL,
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  CONSTRAINT `chk_bp_donation_config_singleton` CHECK (`id` = 1)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='Donation page singleton config';

INSERT INTO `bp_donation_config`
  (`id`,`enabled`,`eyebrow`,`title`,`description`,`scan_title`,`scan_description`,`thanks`,`methods`)
VALUES
  (1,0,'赞赏','赞赏支持','如果 BugPk-Api 对你有帮助，欢迎用你喜欢的方式支持项目。','扫码赞赏','选择你习惯的支付方式，感谢每一份支持。','感谢你的支持，我们会继续把服务做好。',JSON_ARRAY())
ON DUPLICATE KEY UPDATE `id`=VALUES(`id`);

INSERT INTO `bp_menu`
  (`id`,`pid`,`name`,`path`,`component`,`redirect`,`type`,`auth_code`,`meta`,`status`)
VALUES
  (190,120,'DonationConfig','/content/donation','/donation/index',NULL,'menu','donation:config:view',JSON_OBJECT('title','赞赏设置','icon','lucide:heart-handshake','order',8),1),
  (191,190,'DonationConfigUpdate','',NULL,NULL,'button','donation:config:update',JSON_OBJECT('title','保存赞赏设置','order',1),1),
  (192,190,'DonationQRCodeUpload','',NULL,NULL,'button','donation:upload-qr',JSON_OBJECT('title','上传赞赏收款码','order',2),1)
ON DUPLICATE KEY UPDATE
  `pid`=VALUES(`pid`),`path`=VALUES(`path`),`component`=VALUES(`component`),
  `redirect`=VALUES(`redirect`),`type`=VALUES(`type`),`auth_code`=VALUES(`auth_code`),
  `meta`=VALUES(`meta`),`status`=VALUES(`status`);

INSERT IGNORE INTO `bp_role_menu` (`role_id`,`menu_id`)
SELECT 1,`id` FROM `bp_menu` WHERE `id` BETWEEN 190 AND 192;


-- ============================================================
-- Migration: 000055_site_analytics_code.up.sql
-- ============================================================

ALTER TABLE `bp_site_config`
  ADD COLUMN `analytics_code` mediumtext CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci NULL
    COMMENT 'Public analytics and tracking code snippet'
    AFTER `copyright`;


-- ============================================================
-- Migration: 000056_captcha_scene_selection.up.sql
-- ============================================================

-- Keep provider selection (`enabled`) separate from the scene-level switch.
-- This lets an administrator disable a scene without losing its provider or
-- encrypted credentials, and lets it be enabled again without reconfiguration.
ALTER TABLE `bp_captcha_provider_config`
  ADD COLUMN `scene_enabled` tinyint NOT NULL DEFAULT 1
    COMMENT 'Whether this authentication scene requires a captcha'
    AFTER `scene`;

-- Preserve the effective intent of existing installations: a scene with no
-- active provider was already treated as disabled by the old admin setting.
UPDATE `bp_captcha_provider_config` AS config
JOIN (
  SELECT `scene`, MAX(CASE WHEN `enabled` = 1 THEN 1 ELSE 0 END) AS `scene_enabled`
  FROM `bp_captcha_provider_config`
  GROUP BY `scene`
) AS states ON states.`scene` = config.`scene`
SET config.`scene_enabled` = states.`scene_enabled`;


-- ============================================================
-- Migration: 000057_interface_qps_limit.up.sql
-- ============================================================

ALTER TABLE `bp_interface_api`
  ADD COLUMN `qps_limit` int NOT NULL DEFAULT 0
    COMMENT 'Interface-wide QPS hard limit shared by all users and keys; 0 means unlimited'
    AFTER `point_price`;


-- ============================================================
-- Migration: 000058_user_ban_policy.up.sql
-- ============================================================

ALTER TABLE `bp_user`
  ADD COLUMN `ban_permanent` tinyint NOT NULL DEFAULT 0
    COMMENT 'Whether the current account ban is permanent'
    AFTER `status`,
  ADD COLUMN `ban_until` datetime NULL
    COMMENT 'Temporary ban expiry; null for active or permanently banned accounts'
    AFTER `ban_permanent`,
  ADD COLUMN `ban_reason` varchar(500) NOT NULL DEFAULT ''
    COMMENT 'Administrator-provided account ban reason'
    AFTER `ban_until`,
  ADD KEY `idx_bp_user_ban_state` (`status`, `ban_permanent`, `ban_until`);

-- Preserve installations that used status=0 before timed bans existed. Those
-- accounts remain unavailable until an administrator explicitly unbans them.
UPDATE `bp_user`
SET `ban_permanent` = 1,
    `ban_reason` = CASE
      WHEN TRIM(`ban_reason`) = '' THEN 'Legacy disabled account'
      ELSE `ban_reason`
    END
WHERE `status` = 0;


-- ============================================================
-- Migration: 000059_remove_duplicate_dashboard_menus.up.sql
-- ============================================================

-- The administrator workbench is the single management dashboard. Remove the
-- duplicate management entry and the legacy standalone earth-monitor entry;
-- the workbench retains the global/China 2D and 3D atlas, while the risk
-- control screen retains its real-time failure and event views.
DELETE FROM `bp_role_menu`
WHERE `menu_id` IN (91, 171, 173);

DELETE FROM `bp_menu`
WHERE `id` IN (91, 171, 173);


-- ============================================================
-- Migration: 000060_donation_supporters.up.sql
-- ============================================================

CREATE TABLE IF NOT EXISTS `bp_donation_supporter` (
  `id` bigint NOT NULL AUTO_INCREMENT,
  `nickname` varchar(64) NOT NULL COMMENT 'Public supporter nickname',
  `avatar_url` varchar(1024) NOT NULL DEFAULT '' COMMENT 'Public http/https avatar URL',
  `amount` decimal(12,2) NOT NULL COMMENT 'Donation amount in CNY',
  `message` varchar(255) NOT NULL DEFAULT '' COMMENT 'Optional public supporter message',
  `donated_at` datetime NOT NULL COMMENT 'Actual donation time shown publicly',
  `created_by` bigint DEFAULT NULL COMMENT 'Administrator who added this supporter',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  KEY `idx_bp_donation_supporter_time` (`donated_at`,`id`),
  KEY `idx_bp_donation_supporter_creator` (`created_by`),
  CONSTRAINT `chk_bp_donation_supporter_amount` CHECK (`amount` > 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='Public donation supporter list';

INSERT INTO `bp_menu`
  (`id`,`pid`,`name`,`path`,`component`,`redirect`,`type`,`auth_code`,`meta`,`status`)
VALUES
  (193,190,'DonationSupporterList','',NULL,NULL,'button','donation:supporter:list',JSON_OBJECT('title','查看赞赏人','order',3),1),
  (194,190,'DonationSupporterCreate','',NULL,NULL,'button','donation:supporter:create',JSON_OBJECT('title','新增赞赏人','order',4),1)
ON DUPLICATE KEY UPDATE
  `pid`=VALUES(`pid`),`path`=VALUES(`path`),`component`=VALUES(`component`),
  `redirect`=VALUES(`redirect`),`type`=VALUES(`type`),`auth_code`=VALUES(`auth_code`),
  `meta`=VALUES(`meta`),`status`=VALUES(`status`);

INSERT IGNORE INTO `bp_role_menu` (`role_id`,`menu_id`)
SELECT 1,`id` FROM `bp_menu` WHERE `id` BETWEEN 193 AND 194;


-- ============================================================
-- Migration: 000061_remove_home_overview.up.sql
-- ============================================================

-- Workspace is the single dashboard entry. HomeOverview points to the same
-- component and must not remain as a second navigation item.
DELETE FROM `bp_role_menu`
WHERE `menu_id` = 141;

DELETE FROM `bp_menu`
WHERE `id` = 141
   OR `name` = 'HomeOverview'
   OR `path` = '/dashboard/home-overview';


-- ============================================================
-- Migration: 000062_captcha_global_switch.up.sql
-- ============================================================

CREATE TABLE IF NOT EXISTS `bp_captcha_global_config` (
  `id` tinyint NOT NULL DEFAULT 1 COMMENT 'Singleton id',
  `enabled` tinyint NOT NULL DEFAULT 1 COMMENT 'Global captcha switch: 1 enabled, 0 disabled',
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  CONSTRAINT `chk_bp_captcha_global_singleton` CHECK (`id` = 1),
  CONSTRAINT `chk_bp_captcha_global_enabled` CHECK (`enabled` IN (0, 1))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='Global captcha switch';

INSERT INTO `bp_captcha_global_config` (`id`, `enabled`)
VALUES (1, 1)
ON DUPLICATE KEY UPDATE `id` = VALUES(`id`);


-- ============================================================
-- Migration: 000063_ticket_system.up.sql
-- ============================================================

CREATE TABLE IF NOT EXISTS `bp_ticket` (
  `id` bigint NOT NULL AUTO_INCREMENT COMMENT 'Ticket id',
  `ticket_no` varchar(32) NOT NULL COMMENT 'Public ticket number',
  `user_id` bigint NOT NULL COMMENT 'Owner user id',
  `subject` varchar(128) NOT NULL COMMENT 'Ticket subject',
  `category` varchar(32) NOT NULL DEFAULT 'GENERAL' COMMENT 'GENERAL/API/BILLING/ACCOUNT/BUG/OTHER',
  `priority` varchar(16) NOT NULL DEFAULT 'NORMAL' COMMENT 'LOW/NORMAL/HIGH/URGENT',
  `status` varchar(24) NOT NULL DEFAULT 'OPEN' COMMENT 'OPEN/PROCESSING/WAITING_USER/RESOLVED/CLOSED',
  `assigned_to` bigint DEFAULT NULL COMMENT 'Assigned staff user id',
  `last_message_preview` varchar(255) NOT NULL DEFAULT '' COMMENT 'Privacy-safe conversation preview',
  `last_message_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT 'Last message time',
  `last_reply_by` varchar(16) NOT NULL DEFAULT 'USER' COMMENT 'USER/STAFF',
  `message_count` int NOT NULL DEFAULT 1 COMMENT 'Message count',
  `unread_user` int NOT NULL DEFAULT 0 COMMENT 'Unread message count for owner',
  `unread_admin` int NOT NULL DEFAULT 1 COMMENT 'Unread message count for staff',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_bp_ticket_no` (`ticket_no`),
  KEY `idx_bp_ticket_user_status` (`user_id`, `status`, `last_message_at`),
  KEY `idx_bp_ticket_status_priority` (`status`, `priority`, `last_message_at`),
  KEY `idx_bp_ticket_assigned` (`assigned_to`, `status`, `last_message_at`),
  CONSTRAINT `chk_bp_ticket_priority` CHECK (`priority` IN ('LOW','NORMAL','HIGH','URGENT')),
  CONSTRAINT `chk_bp_ticket_status` CHECK (`status` IN ('OPEN','PROCESSING','WAITING_USER','RESOLVED','CLOSED')),
  CONSTRAINT `chk_bp_ticket_reply_by` CHECK (`last_reply_by` IN ('USER','STAFF'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='Support tickets';

CREATE TABLE IF NOT EXISTS `bp_ticket_message` (
  `id` bigint NOT NULL AUTO_INCREMENT COMMENT 'Message id',
  `ticket_id` bigint NOT NULL COMMENT 'Ticket id',
  `sender_id` bigint NOT NULL COMMENT 'Sender user id',
  `sender_type` varchar(16) NOT NULL COMMENT 'USER/STAFF',
  `content` text NOT NULL COMMENT 'Plain-text message body',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  KEY `idx_bp_ticket_message_ticket` (`ticket_id`, `id`),
  KEY `idx_bp_ticket_message_sender` (`sender_id`, `created_at`),
  CONSTRAINT `chk_bp_ticket_message_sender_type` CHECK (`sender_type` IN ('USER','STAFF'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='Ticket conversation messages';

INSERT INTO `bp_menu`
  (`id`, `pid`, `name`, `path`, `component`, `redirect`, `type`, `auth_code`, `meta`, `status`)
VALUES
  (195, 0, 'TicketCenter', '/tickets', '/ticket/index', NULL, 'menu', 'ticket:user:list', JSON_OBJECT('title', 'ticket.title', 'icon', 'lucide:messages-square', 'order', 9988), 1),
  (196, 195, 'TicketCreate', '', NULL, NULL, 'button', 'ticket:user:create', JSON_OBJECT('title', 'ticket.permissions.create', 'order', 1), 1),
  (197, 195, 'TicketReply', '', NULL, NULL, 'button', 'ticket:user:reply', JSON_OBJECT('title', 'ticket.permissions.reply', 'order', 2), 1),
  (198, 195, 'TicketAdminList', '', NULL, NULL, 'button', 'ticket:admin:list', JSON_OBJECT('title', 'ticket.permissions.adminList', 'order', 3), 1),
  (199, 195, 'TicketAdminReply', '', NULL, NULL, 'button', 'ticket:admin:reply', JSON_OBJECT('title', 'ticket.permissions.adminReply', 'order', 4), 1),
  (200, 195, 'TicketAdminUpdate', '', NULL, NULL, 'button', 'ticket:admin:update', JSON_OBJECT('title', 'ticket.permissions.adminUpdate', 'order', 5), 1)
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
SELECT 1, `id` FROM `bp_menu` WHERE `id` BETWEEN 195 AND 200;

INSERT IGNORE INTO `bp_role_menu` (`role_id`, `menu_id`)
SELECT 2, `id` FROM `bp_menu` WHERE `id` BETWEEN 195 AND 197;


-- ============================================================
-- Migration: 000064_site_branding.up.sql
-- ============================================================

-- ============================================================
-- Migration 000064: add open-source branding columns to bp_site_config
-- Adds repository, author, and license fields for dynamic site branding.
-- ============================================================

ALTER TABLE `bp_site_config`
  ADD COLUMN `github_url` varchar(1024) NOT NULL DEFAULT '' COMMENT 'GitHub repository URL' AFTER `site_url`,
  ADD COLUMN `author_name` varchar(128) NOT NULL DEFAULT '' COMMENT 'Open-source author or organization name' AFTER `github_url`,
  ADD COLUMN `author_url` varchar(1024) NOT NULL DEFAULT '' COMMENT 'Author homepage or profile URL' AFTER `author_name`,
  ADD COLUMN `license_type` varchar(64) NOT NULL DEFAULT 'AGPL-3.0' COMMENT 'Open-source license type (e.g. MIT, Apache-2.0)' AFTER `author_url`,
  ADD COLUMN `license_url` varchar(1024) NOT NULL DEFAULT '' COMMENT 'Full license text URL' AFTER `license_type`;

-- 仓库地址由部署者在后台填写，仓库地址与作者链接不写死在迁移里。
UPDATE `bp_site_config`
SET `author_name` = 'BugPk-Api',
    `license_type` = 'AGPL-3.0'
WHERE `id` = 1;


-- ============================================================
-- Migration: 000065_site_loading_animation_presets.up.sql
-- ============================================================

ALTER TABLE `bp_site_config`
  ADD COLUMN `loading_animation_preset` varchar(32) NOT NULL DEFAULT 'gateway'
    COMMENT 'Built-in startup animation preset and custom image fallback'
    AFTER `loading_animation_enabled`;


-- ============================================================
-- Migration: 000066_risk_account_ban_permission.up.sql
-- ============================================================

-- Dedicated permission for the one-click account ban action on the risk dashboard.
INSERT INTO `bp_menu`
  (`id`, `pid`, `name`, `path`, `component`, `redirect`, `type`, `auth_code`, `meta`, `status`)
VALUES
  (201, 170, 'RiskControlUserBan', '', NULL, NULL, 'button', 'risk-control:user:ban', JSON_OBJECT('title', 'system.riskControl.permissions.banUser', 'order', 2), 1)
ON DUPLICATE KEY UPDATE
  `pid` = VALUES(`pid`),
  `auth_code` = VALUES(`auth_code`),
  `meta` = VALUES(`meta`),
  `status` = VALUES(`status`);

INSERT IGNORE INTO `bp_role_menu` (`role_id`, `menu_id`)
SELECT 1, 201;


-- ============================================================
-- Migration: 000067_role_qps_registration.up.sql
-- ============================================================

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


-- ============================================================
-- Migration: 000068_navigation_and_ticket_routes.up.sql
-- ============================================================

-- Align route paths with the console URLs used by the workbench and provide
-- role-specific navigation without duplicating shared feature routes.
UPDATE `bp_menu`
SET `path` = '/console/interfaces'
WHERE `id` = 41 AND `name` = 'InterfaceList';

UPDATE `bp_menu`
SET `path` = '/console/call-logs'
WHERE `id` = 50 AND `name` = 'InterfaceCallLog';

UPDATE `bp_menu`
SET `meta` = JSON_SET(
  COALESCE(`meta`, JSON_OBJECT()),
  '$.adminTitle', 'navigation.admin.interfaceGateway',
  '$.adminOrder', 2,
  '$.userTitle', 'navigation.user.interfaceServices',
  '$.userOrder', 2
)
WHERE `id` = 40 AND `name` = 'InterfaceManagement';

UPDATE `bp_menu`
SET `pid` = 40,
    `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.order', 3)
WHERE `id` = 60 AND `name` = 'ApiKeyManagement';

UPDATE `bp_menu`
SET `meta` = JSON_SET(
  COALESCE(`meta`, JSON_OBJECT()),
  '$.title', 'navigation.admin.userPermissions',
  '$.order', 3,
  '$.audience', 'admin'
)
WHERE `id` = 10 AND `name` = 'UserManagement';

UPDATE `bp_menu`
SET `meta` = JSON_SET(
  COALESCE(`meta`, JSON_OBJECT()),
  '$.title', 'navigation.admin.packagesTransactions',
  '$.order', 4,
  '$.audience', 'admin'
)
WHERE `id` = 70 AND `name` = 'PackageManagement';

UPDATE `bp_menu`
SET `pid` = 70,
    `meta` = JSON_SET(
      COALESCE(`meta`, JSON_OBJECT()),
      '$.order', 5,
      '$.audience', 'admin'
    )
WHERE `id` = 100 AND `name` = 'PaymentManagement';

UPDATE `bp_menu`
SET `meta` = JSON_SET(
  COALESCE(`meta`, JSON_OBJECT()),
  '$.title', 'navigation.user.packagesAssets',
  '$.order', 3,
  '$.audience', 'user'
)
WHERE `id` = 84 AND `name` = 'PackageMall';

UPDATE `bp_menu`
SET `meta` = JSON_SET(
  COALESCE(`meta`, JSON_OBJECT()),
  '$.title', 'navigation.admin.riskMonitoring',
  '$.order', 5,
  '$.audience', 'admin'
)
WHERE `id` = 90 AND `name` = 'DashboardStats';

UPDATE `bp_menu`
SET `meta` = JSON_SET(
  COALESCE(`meta`, JSON_OBJECT()),
  '$.order', 7,
  '$.audience', 'admin'
)
WHERE `id` = 120 AND `name` = 'ContentOperations';

UPDATE `bp_menu`
SET `meta` = JSON_SET(
  COALESCE(`meta`, JSON_OBJECT()),
  '$.order', 8,
  '$.audience', 'admin'
)
WHERE `id` = 30 AND `name` = 'System';

INSERT INTO `bp_menu`
  (`id`, `pid`, `name`, `path`, `component`, `redirect`, `type`, `auth_code`, `meta`, `status`)
VALUES
  (210, 0, 'UserServiceSupport', '/console/support', NULL, '/console/tickets', 'catalog', NULL, JSON_OBJECT('title', 'navigation.user.serviceSupport', 'icon', 'lucide:life-buoy', 'order', 4, 'audience', 'user'), 1),
  (202, 210, 'UserNoticeCenter', '/notice', '/notice/user/list', NULL, 'menu', NULL, JSON_OBJECT('title', 'system.notice.userTitle', 'icon', 'mdi:bullhorn-outline', 'order', 2, 'audience', 'user'), 1),
  (203, 0, 'Profile', '/profile', '/_core/profile/index', NULL, 'menu', NULL, JSON_OBJECT('title', 'page.auth.profile', 'icon', 'lucide:user-cog', 'order', 5, 'audience', 'user', 'hideInMenu', false), 1),
  (204, 0, 'AdminTicketManagement', '/admin/tickets', '/ticket/admin', NULL, 'menu', 'ticket:admin:list', JSON_OBJECT('title', 'ticket.adminTitle', 'icon', 'lucide:message-square-reply', 'order', 6, 'audience', 'admin'), 1)
ON DUPLICATE KEY UPDATE
  `pid` = VALUES(`pid`),
  `path` = VALUES(`path`),
  `component` = VALUES(`component`),
  `redirect` = VALUES(`redirect`),
  `type` = VALUES(`type`),
  `auth_code` = VALUES(`auth_code`),
  `meta` = VALUES(`meta`),
  `status` = VALUES(`status`);

UPDATE `bp_menu`
SET `pid` = 210,
    `name` = 'UserTicketCenter',
    `path` = '/console/tickets',
    `component` = '/ticket/user',
    `meta` = JSON_OBJECT('title', 'ticket.title', 'icon', 'lucide:messages-square', 'order', 1, 'audience', 'user')
WHERE `id` = 195;

UPDATE `bp_menu`
SET `pid` = 204
WHERE `id` BETWEEN 198 AND 200;

INSERT IGNORE INTO `bp_role_menu` (`role_id`, `menu_id`)
SELECT r.`id`, m.`id`
FROM `bp_role` r
JOIN `bp_menu` m ON m.`id` IN (195, 196, 197, 202, 203, 210)
WHERE r.`role_key` = 'user';

INSERT IGNORE INTO `bp_role_menu` (`role_id`, `menu_id`)
SELECT r.`id`, m.`id`
FROM `bp_role` r
JOIN `bp_menu` m ON m.`id` IN (198, 199, 200, 204)
WHERE r.`role_key` = 'admin';


-- ============================================================
-- Migration: 000069_ticket_enhancements.up.sql
-- ============================================================

ALTER TABLE `bp_ticket`
  ADD COLUMN `tags` json DEFAULT NULL COMMENT 'Administrator-maintained ticket labels' AFTER `assigned_to`,
  ADD COLUMN `language` varchar(8) NOT NULL DEFAULT 'zh' COMMENT 'Notification language snapshot' AFTER `tags`,
  ADD COLUMN `first_response_due_at` datetime DEFAULT NULL COMMENT 'First staff response SLA deadline' AFTER `language`,
  ADD COLUMN `resolution_due_at` datetime DEFAULT NULL COMMENT 'Resolution SLA deadline' AFTER `first_response_due_at`,
  ADD COLUMN `first_responded_at` datetime DEFAULT NULL COMMENT 'First staff response time' AFTER `resolution_due_at`,
  ADD COLUMN `resolved_at` datetime DEFAULT NULL COMMENT 'First resolved time' AFTER `first_responded_at`,
  ADD COLUMN `closed_at` datetime DEFAULT NULL COMMENT 'Closed time' AFTER `resolved_at`,
  ADD KEY `idx_bp_ticket_first_response_sla` (`first_response_due_at`, `first_responded_at`, `status`),
  ADD KEY `idx_bp_ticket_resolution_sla` (`resolution_due_at`, `resolved_at`, `status`);

ALTER TABLE `bp_ticket_message`
  ADD COLUMN `internal_note` tinyint NOT NULL DEFAULT 0 COMMENT 'Staff-only internal note' AFTER `sender_type`,
  ADD KEY `idx_bp_ticket_message_visibility` (`ticket_id`, `internal_note`, `id`);

UPDATE `bp_ticket`
SET `first_response_due_at` = CASE `priority`
      WHEN 'URGENT' THEN DATE_ADD(`created_at`, INTERVAL 15 MINUTE)
      WHEN 'HIGH' THEN DATE_ADD(`created_at`, INTERVAL 1 HOUR)
      WHEN 'LOW' THEN DATE_ADD(`created_at`, INTERVAL 8 HOUR)
      ELSE DATE_ADD(`created_at`, INTERVAL 4 HOUR)
    END,
    `resolution_due_at` = CASE `priority`
      WHEN 'URGENT' THEN DATE_ADD(`created_at`, INTERVAL 4 HOUR)
      WHEN 'HIGH' THEN DATE_ADD(`created_at`, INTERVAL 12 HOUR)
      WHEN 'LOW' THEN DATE_ADD(`created_at`, INTERVAL 72 HOUR)
      ELSE DATE_ADD(`created_at`, INTERVAL 48 HOUR)
    END,
    `resolved_at` = CASE WHEN `status` IN ('RESOLVED', 'CLOSED') THEN `updated_at` ELSE NULL END,
    `closed_at` = CASE WHEN `status` = 'CLOSED' THEN `updated_at` ELSE NULL END
WHERE `first_response_due_at` IS NULL OR `resolution_due_at` IS NULL;

CREATE TABLE IF NOT EXISTS `bp_ticket_attachment` (
  `id` bigint NOT NULL AUTO_INCREMENT,
  `ticket_id` bigint NOT NULL,
  `message_id` bigint DEFAULT NULL,
  `uploader_id` bigint NOT NULL,
  `original_name` varchar(255) NOT NULL,
  `stored_name` varchar(96) NOT NULL,
  `content_type` varchar(128) NOT NULL,
  `size_bytes` bigint NOT NULL,
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_bp_ticket_attachment_stored` (`stored_name`),
  KEY `idx_bp_ticket_attachment_ticket` (`ticket_id`, `id`),
  KEY `idx_bp_ticket_attachment_message` (`message_id`, `id`),
  KEY `idx_bp_ticket_attachment_uploader` (`uploader_id`, `created_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='Private ticket attachments';

CREATE TABLE IF NOT EXISTS `bp_ticket_activity` (
  `id` bigint NOT NULL AUTO_INCREMENT,
  `ticket_id` bigint NOT NULL,
  `actor_id` bigint NOT NULL,
  `actor_type` varchar(16) NOT NULL COMMENT 'USER/STAFF/SYSTEM',
  `action` varchar(48) NOT NULL,
  `visibility` varchar(16) NOT NULL DEFAULT 'PUBLIC' COMMENT 'PUBLIC/INTERNAL',
  `detail` json DEFAULT NULL,
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  KEY `idx_bp_ticket_activity_ticket` (`ticket_id`, `id`),
  KEY `idx_bp_ticket_activity_action` (`action`, `created_at`),
  CONSTRAINT `chk_bp_ticket_activity_visibility` CHECK (`visibility` IN ('PUBLIC','INTERNAL'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='Ticket activity audit trail';

CREATE TABLE IF NOT EXISTS `bp_ticket_quick_reply` (
  `id` bigint NOT NULL AUTO_INCREMENT,
  `title` varchar(80) NOT NULL,
  `content_zh` text NOT NULL,
  `content_en` text NOT NULL,
  `category` varchar(32) NOT NULL DEFAULT 'GENERAL',
  `sort_no` int NOT NULL DEFAULT 0,
  `status` tinyint NOT NULL DEFAULT 1,
  `created_by` bigint DEFAULT NULL,
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  KEY `idx_bp_ticket_quick_reply_status` (`status`, `sort_no`, `id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='Administrator ticket quick replies';

INSERT INTO `bp_ticket_quick_reply`
  (`id`, `title`, `content_zh`, `content_en`, `category`, `sort_no`, `status`)
VALUES
  (1, '需要更多信息', '您好，为了进一步定位问题，请补充接口名称、请求时间、错误信息和可复现步骤，并注意不要提交完整 API Key。', 'Hello. To investigate further, please provide the API name, request time, error message, and reproduction steps. Do not include a complete API key.', 'GENERAL', 10, 1),
  (2, '问题已处理', '您好，相关问题已经处理，请重新尝试。如仍有异常，请在本工单中继续回复。', 'Hello. The issue has been addressed. Please try again and reply to this ticket if the problem continues.', 'GENERAL', 20, 1),
  (3, '等待用户确认', '您好，当前已完成初步处理，请您确认问题是否恢复；确认后我们会关闭工单。', 'Hello. Initial remediation is complete. Please confirm whether the issue is resolved so we can close the ticket.', 'GENERAL', 30, 1)
ON DUPLICATE KEY UPDATE
  `title` = VALUES(`title`),
  `content_zh` = VALUES(`content_zh`),
  `content_en` = VALUES(`content_en`),
  `category` = VALUES(`category`),
  `sort_no` = VALUES(`sort_no`),
  `status` = VALUES(`status`);

INSERT INTO `bp_menu`
  (`id`, `pid`, `name`, `path`, `component`, `redirect`, `type`, `auth_code`, `meta`, `status`)
VALUES
  (205, 195, 'TicketUserClose', '', NULL, NULL, 'button', 'ticket:user:close', JSON_OBJECT('title', 'ticket.permissions.close', 'order', 3), 1),
  (206, 195, 'TicketUserAttachment', '', NULL, NULL, 'button', 'ticket:user:attachment', JSON_OBJECT('title', 'ticket.permissions.attachment', 'order', 4), 1),
  (207, 204, 'TicketAdminNote', '', NULL, NULL, 'button', 'ticket:admin:note', JSON_OBJECT('title', 'ticket.permissions.adminNote', 'order', 4), 1),
  (208, 204, 'TicketAdminStats', '', NULL, NULL, 'button', 'ticket:admin:stats', JSON_OBJECT('title', 'ticket.permissions.adminStats', 'order', 5), 1),
  (209, 204, 'TicketAdminQuickReply', '', NULL, NULL, 'button', 'ticket:admin:quick-reply', JSON_OBJECT('title', 'ticket.permissions.adminQuickReply', 'order', 6), 1),
  (210, 204, 'TicketAdminAttachment', '', NULL, NULL, 'button', 'ticket:admin:attachment', JSON_OBJECT('title', 'ticket.permissions.attachment', 'order', 7), 1)
ON DUPLICATE KEY UPDATE
  `pid` = VALUES(`pid`),
  `type` = VALUES(`type`),
  `auth_code` = VALUES(`auth_code`),
  `meta` = VALUES(`meta`),
  `status` = VALUES(`status`);

INSERT IGNORE INTO `bp_role_menu` (`role_id`, `menu_id`)
SELECT r.`id`, m.`id`
FROM `bp_role` r
JOIN `bp_menu` m ON m.`id` IN (205, 206)
WHERE r.`role_key` = 'user';

INSERT IGNORE INTO `bp_role_menu` (`role_id`, `menu_id`)
SELECT r.`id`, m.`id`
FROM `bp_role` r
JOIN `bp_menu` m ON m.`id` BETWEEN 207 AND 210
WHERE r.`role_key` = 'admin';


-- ============================================================
-- Migration: 000070_notification_outbox.up.sql
-- ============================================================

CREATE TABLE IF NOT EXISTS `bp_notification_outbox` (
  `id` bigint NOT NULL AUTO_INCREMENT,
  `channel` varchar(24) NOT NULL DEFAULT 'EMAIL',
  `template_key` varchar(80) NOT NULL,
  `recipient` varchar(255) NOT NULL,
  `language` varchar(8) NOT NULL DEFAULT 'zh',
  `payload` json NOT NULL,
  `dedupe_key` varchar(191) NOT NULL,
  `status` varchar(24) NOT NULL DEFAULT 'PENDING',
  `attempts` int NOT NULL DEFAULT 0,
  `max_attempts` int NOT NULL DEFAULT 12,
  `next_attempt_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `locked_at` datetime DEFAULT NULL,
  `last_error` varchar(1000) NOT NULL DEFAULT '',
  `sent_at` datetime DEFAULT NULL,
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_bp_notification_outbox_dedupe` (`dedupe_key`),
  KEY `idx_bp_notification_outbox_pending` (`status`, `next_attempt_at`, `id`),
  KEY `idx_bp_notification_outbox_recipient` (`recipient`, `created_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='Asynchronous notification outbox';


-- ============================================================
-- Migration: 000071_friend_link_review_workflow.up.sql
-- ============================================================

ALTER TABLE `bp_friend_link_application`
  MODIFY COLUMN `reject_reason` varchar(1000) NOT NULL DEFAULT '',
  ADD COLUMN `language` varchar(8) NOT NULL DEFAULT 'zh' COMMENT 'Applicant page language snapshot' AFTER `backlink_url`,
  ADD COLUMN `review_reason_template` varchar(64) NOT NULL DEFAULT '' COMMENT 'Review reason template key' AFTER `reject_reason`,
  ADD COLUMN `review_send_email` tinyint NOT NULL DEFAULT 1 COMMENT 'Whether review email was requested' AFTER `review_reason_template`;

ALTER TABLE `bp_friend_link`
  ADD COLUMN `language` varchar(8) NOT NULL DEFAULT 'zh' COMMENT 'Applicant language snapshot' AFTER `applicant_email`,
  ADD COLUMN `source_application_id` bigint DEFAULT NULL COMMENT 'Source application id' AFTER `language`,
  ADD KEY `idx_bp_friend_link_source_application` (`source_application_id`);

CREATE TABLE IF NOT EXISTS `bp_friend_link_audit` (
  `id` bigint NOT NULL AUTO_INCREMENT,
  `entity_type` varchar(24) NOT NULL COMMENT 'APPLICATION/LINK',
  `entity_id` bigint NOT NULL,
  `action` varchar(24) NOT NULL COMMENT 'APPLY/APPROVE/REJECT/DELETE',
  `actor_id` bigint DEFAULT NULL,
  `actor_name` varchar(64) NOT NULL DEFAULT '',
  `reason` varchar(1000) NOT NULL DEFAULT '',
  `reason_template` varchar(64) NOT NULL DEFAULT '',
  `send_email` tinyint NOT NULL DEFAULT 0,
  `language` varchar(8) NOT NULL DEFAULT 'zh',
  `snapshot` json DEFAULT NULL,
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  KEY `idx_bp_friend_link_audit_entity` (`entity_type`, `entity_id`, `id`),
  KEY `idx_bp_friend_link_audit_actor` (`actor_id`, `created_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='Friend link application and removal audit trail';

INSERT INTO `bp_captcha_provider_config`
  (`provider_key`, `scene`, `enabled`, `timeout_ms`, `failure_policy`, `extra_config`)
VALUES
  ('pow', 'friend_link_apply', 1, 3000, 'deny', JSON_OBJECT('difficulty', 3))
ON DUPLICATE KEY UPDATE
  `enabled` = VALUES(`enabled`),
  `extra_config` = VALUES(`extra_config`);


-- ============================================================
-- Migration: 000072_risk_dashboard_permission_repair.up.sql
-- ============================================================

-- 000068 originally reused menu id 201 for the user support catalog, which
-- overwrote RiskControlUserBan from 000066. Move that catalog to id 210 and
-- restore all administrator-only risk dashboard permissions by role key.

INSERT INTO `bp_menu`
  (`id`, `pid`, `name`, `path`, `component`, `redirect`, `type`, `auth_code`, `meta`, `status`)
VALUES
  (210, 0, 'UserServiceSupport', '/console/support', NULL, '/console/tickets', 'catalog', NULL, JSON_OBJECT('title', 'navigation.user.serviceSupport', 'icon', 'lucide:life-buoy', 'order', 4, 'audience', 'user'), 1)
ON DUPLICATE KEY UPDATE
  `pid` = VALUES(`pid`),
  `name` = VALUES(`name`),
  `path` = VALUES(`path`),
  `component` = VALUES(`component`),
  `redirect` = VALUES(`redirect`),
  `type` = VALUES(`type`),
  `auth_code` = VALUES(`auth_code`),
  `meta` = VALUES(`meta`),
  `status` = VALUES(`status`);

UPDATE `bp_menu`
SET `pid` = 210
WHERE (`id` = 195 AND `name` IN ('TicketCenter', 'UserTicketCenter'))
   OR (`id` = 202 AND `name` = 'UserNoticeCenter');

DELETE FROM `bp_role_menu`
WHERE `menu_id` = 201;

DELETE FROM `bp_menu`
WHERE `id` = 201;

INSERT INTO `bp_menu`
  (`id`, `pid`, `name`, `path`, `component`, `redirect`, `type`, `auth_code`, `meta`, `status`)
VALUES
  (201, 170, 'RiskControlUserBan', '', NULL, NULL, 'button', 'risk-control:user:ban', JSON_OBJECT('title', 'system.riskControl.permissions.banUser', 'order', 2), 1)
ON DUPLICATE KEY UPDATE
  `pid` = VALUES(`pid`),
  `name` = VALUES(`name`),
  `path` = VALUES(`path`),
  `component` = VALUES(`component`),
  `redirect` = VALUES(`redirect`),
  `type` = VALUES(`type`),
  `auth_code` = VALUES(`auth_code`),
  `meta` = VALUES(`meta`),
  `status` = VALUES(`status`);

INSERT IGNORE INTO `bp_role_menu` (`role_id`, `menu_id`)
SELECT r.`id`, m.`id`
FROM `bp_role` r
JOIN `bp_menu` m ON m.`id` IN (170, 172, 201)
WHERE r.`role_key` = 'admin';

INSERT IGNORE INTO `bp_role_menu` (`role_id`, `menu_id`)
SELECT r.`id`, m.`id`
FROM `bp_role` r
JOIN `bp_menu` m ON m.`id` IN (195, 202, 210)
WHERE r.`role_key` = 'user';


-- ============================================================
-- Migration: 000073_call_log_upstream_url_capacity.up.sql
-- ============================================================

ALTER TABLE `bp_interface_call_log`
  MODIFY COLUMN `upstream_url` TEXT DEFAULT NULL COMMENT '上游节点引用（不可逆脱敏）';


-- ============================================================
-- Migration: 000074_key_menu_flatten.up.sql
-- ============================================================

UPDATE `bp_menu`
SET `pid` = 0,
    `path` = '/key',
    `component` = '/key/list',
    `type` = 'menu',
    `auth_code` = 'key:list',
    `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.title', 'system.key.title')
WHERE `id` = 60 AND `name` = 'ApiKeyManagement';

INSERT IGNORE INTO `bp_role_menu` (`role_id`, `menu_id`)
SELECT `role_id`, 60
FROM `bp_role_menu`
WHERE `menu_id` = 61;

UPDATE `bp_menu`
SET `pid` = 60
WHERE `pid` = 61;

DELETE FROM `bp_role_menu`
WHERE `menu_id` = 61;

DELETE FROM `bp_menu`
WHERE `id` = 61 AND `name` = 'ApiKeyList';


-- ============================================================
-- Migration: 000075_ticket_admin_attachment_permission.up.sql
-- ============================================================

-- Migration 000069 temporarily assigned menu id 210 to the administrator
-- ticket-attachment permission. Migration 000072 later reused that id for the
-- user support catalog, so restore the administrator permission under a
-- collision-free id.
INSERT INTO `bp_menu`
  (`pid`, `name`, `path`, `component`, `redirect`, `type`, `auth_code`, `meta`, `status`)
SELECT
  admin_ticket.`id`, 'TicketAdminAttachment', '', NULL, NULL, 'button',
  'ticket:admin:attachment',
  JSON_OBJECT('title', 'ticket.permissions.attachment', 'order', 7),
  1
FROM `bp_menu` admin_ticket
WHERE admin_ticket.`name` = 'AdminTicketManagement'
  AND NOT EXISTS (
    SELECT 1 FROM `bp_menu` existing
    WHERE existing.`name` = 'TicketAdminAttachment'
  );

UPDATE `bp_menu` attachment
JOIN `bp_menu` admin_ticket ON admin_ticket.`name` = 'AdminTicketManagement'
SET attachment.`pid` = admin_ticket.`id`,
    attachment.`path` = '',
    attachment.`component` = NULL,
    attachment.`redirect` = NULL,
    attachment.`type` = 'button',
    attachment.`auth_code` = 'ticket:admin:attachment',
    attachment.`meta` = JSON_OBJECT('title', 'ticket.permissions.attachment', 'order', 7),
    attachment.`status` = 1
WHERE attachment.`name` = 'TicketAdminAttachment';

INSERT IGNORE INTO `bp_role_menu` (`role_id`, `menu_id`)
SELECT r.`id`, m.`id`
FROM `bp_role` r
JOIN `bp_menu` m ON m.`name` = 'TicketAdminAttachment'
WHERE r.`role_key` = 'admin';


-- ============================================================
-- Migration: 000076_call_log_analytics_indexes.up.sql
-- ============================================================

ALTER TABLE `bp_interface_call_log`
  ADD KEY `idx_bp_call_log_home_window` (
    `created_at`,
    `charge_type`,
    `user_id`,
    `success`,
    `ip_province`,
    `ip_city`,
    `client_ip`
  ),
  ADD KEY `idx_bp_call_log_access_window` (
    `created_at`,
    `charge_type`,
    `user_id`,
    `ip_country`,
    `ip_province`,
    `ip_city`,
    `ip_isp`,
    `latitude`,
    `longitude`
  ),
  ADD KEY `idx_bp_call_log_failure_ip` (
    `created_at`,
    `charge_type`,
    `success`,
    `client_ip`,
    `interface_id`
  ),
  ADD KEY `idx_bp_call_log_failure_user` (
    `created_at`,
    `charge_type`,
    `success`,
    `user_id`,
    `interface_id`
  );


-- ============================================================
-- Migration: 000077_navigation_permission_cleanup.up.sql
-- ============================================================

-- Canonicalize the shared self-service routes and repair permissions by role
-- key.  Do not depend on built-in role ids: upgraded installations may have
-- created the admin/user roles under different primary keys.

UPDATE `bp_menu`
SET `pid` = 0,
    `path` = '/key',
    `component` = '/key/list',
    `type` = 'menu',
    `auth_code` = 'key:list',
    `meta` = JSON_SET(
      COALESCE(`meta`, JSON_OBJECT()),
      '$.title', 'system.key.title',
      '$.adminTitle', 'system.key.title',
      '$.adminOrder', 3,
      '$.userTitle', 'system.key.myKeys',
      '$.userOrder', 3,
      '$.audience', 'all'
    ),
    `status` = 1
WHERE `id` = 60 AND `name` = 'ApiKeyManagement';

-- Some installations still contain the former catalog -> list pair.  Move
-- both role grants and action buttons to the promoted menu before removing it.
INSERT IGNORE INTO `bp_role_menu` (`role_id`, `menu_id`)
SELECT `role_id`, 60
FROM `bp_role_menu`
WHERE `menu_id` = 61
  AND EXISTS (
    SELECT 1 FROM `bp_menu`
    WHERE `id` = 60 AND `name` = 'ApiKeyManagement'
  );

UPDATE `bp_menu`
SET `pid` = 60
WHERE `pid` = 61;

DELETE FROM `bp_role_menu`
WHERE `menu_id` = 61;

DELETE FROM `bp_menu`
WHERE `id` = 61 AND `name` = 'ApiKeyList';

UPDATE `bp_menu`
SET `path` = '/console/interfaces',
    `component` = '/interface/list',
    `auth_code` = 'interface:list',
    `meta` = JSON_SET(
      COALESCE(`meta`, JSON_OBJECT()),
      '$.adminOrder', 1,
      '$.userOrder', 2,
      '$.audience', 'all'
    ),
    `status` = 1
WHERE `id` = 41 AND `name` = 'InterfaceList';

UPDATE `bp_menu`
SET `path` = '/console/call-logs',
    `component` = '/interface/log',
    `auth_code` = 'log:call:list',
    `meta` = JSON_SET(
      COALESCE(`meta`, JSON_OBJECT()),
      '$.adminOrder', 2,
      '$.userOrder', 2.1,
      '$.audience', 'all'
    ),
    `status` = 1
WHERE `id` = 50 AND `name` = 'InterfaceCallLog';

-- The management catalog is administrator-only.  User navigation receives
-- the two canonical self-service routes above as direct top-level entries.
UPDATE `bp_menu`
SET `meta` = JSON_SET(
      COALESCE(`meta`, JSON_OBJECT()),
      '$.audience', 'admin',
      '$.navigationCleanup', '000077'
    )
WHERE `id` = 40 AND `name` = 'InterfaceManagement';

-- Disable obsolete routes without deleting them.  A marker makes rollback
-- target only rows changed by this migration.
UPDATE `bp_menu`
SET `status` = 0,
    `meta` = JSON_SET(
      COALESCE(`meta`, JSON_OBJECT()),
      '$.hideInMenu', true,
      '$.navigationCleanup', '000077-legacy-route'
    )
WHERE `status` = 1
  AND (
    `path` = '/console/apis'
    OR (
      `id` <> 41
      AND `path` IN ('/console/interfaces', '/interface/list')
    )
    OR (
      `id` <> 50
      AND `path` IN ('/console/call-logs', '/interface/log')
    )
  );

UPDATE `bp_menu`
SET `pid` = 0,
    `path` = '/system/about',
    `component` = '/system/about/index',
    `type` = 'menu',
    `auth_code` = 'system:about:view',
    `meta` = JSON_SET(
      COALESCE(`meta`, JSON_OBJECT()),
      '$.title', 'system.about.title',
      '$.order', 2147483647,
      '$.audience', 'all',
      '$.hideInMenu', false
    ),
    `status` = 1
WHERE `id` = 178 OR `name` = 'SystemAbout' OR `path` = '/system/about';

-- Ordinary users receive the API catalogue and masked call-log routes from
-- the scoped menu injector.  They must not inherit the administrator list
-- permissions from the historical shared rows, otherwise the frontend will
-- select management endpoints instead of /market and /user/call-logs.
DELETE rm
FROM `bp_role_menu` rm
JOIN `bp_role` r ON r.`id` = rm.`role_id`
WHERE r.`role_key` = 'user'
  AND rm.`menu_id` IN (40, 41, 50);

INSERT IGNORE INTO `bp_role_menu` (`role_id`, `menu_id`)
SELECT r.`id`, m.`id`
FROM `bp_role` r
JOIN `bp_menu` m ON m.`id` IN (40, 41, 50, 60, 62, 63, 64, 65, 66, 67, 178)
WHERE r.`role_key` = 'admin';

INSERT IGNORE INTO `bp_role_menu` (`role_id`, `menu_id`)
SELECT r.`id`, m.`id`
FROM `bp_role` r
JOIN `bp_menu` m ON m.`id` IN (60, 62, 63, 64, 65, 66, 67, 178)
WHERE r.`role_key` = 'user';


-- ============================================================
-- Migration: 000078_call_log_rollups.up.sql
-- ============================================================

-- Incremental call-log rollups. The user-grained tables intentionally retain
-- contributions from every existing user and exclude only ADMIN traffic.
-- Readers join bp_user and apply the effective-account-status predicate at
-- query time, so a later ban/unban is reflected without rebuilding history.

CREATE TABLE IF NOT EXISTS `bp_call_metric_minute` (
  `bucket_start` datetime NOT NULL COMMENT 'UTC/local database minute bucket',
  `call_count` bigint unsigned NOT NULL DEFAULT 0,
  `success_count` bigint unsigned NOT NULL DEFAULT 0,
  `failure_count` bigint unsigned NOT NULL DEFAULT 0,
  `billable_count` bigint unsigned NOT NULL DEFAULT 0,
  `charge_amount` decimal(38,4) NOT NULL DEFAULT 0.0000,
  `point_amount` decimal(38,0) NOT NULL DEFAULT 0,
  `request_bytes` decimal(38,0) NOT NULL DEFAULT 0,
  `response_bytes` decimal(38,0) NOT NULL DEFAULT 0,
  `elapsed_sum` decimal(38,0) NOT NULL DEFAULT 0,
  `elapsed_min` bigint unsigned NOT NULL DEFAULT 0,
  `elapsed_max` bigint unsigned NOT NULL DEFAULT 0,
  `first_seen` datetime NOT NULL,
  `last_seen` datetime NOT NULL,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`bucket_start`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='Operational all-user call metrics by minute; active-account totals come from bp_call_user_metric_minute';

CREATE TABLE IF NOT EXISTS `bp_call_api_metric_minute` (
  `bucket_start` datetime NOT NULL,
  `interface_id` bigint NOT NULL,
  `user_id` bigint NOT NULL,
  `api_code` varchar(64) NOT NULL DEFAULT '',
  `call_count` bigint unsigned NOT NULL DEFAULT 0,
  `success_count` bigint unsigned NOT NULL DEFAULT 0,
  `failure_count` bigint unsigned NOT NULL DEFAULT 0,
  `billable_count` bigint unsigned NOT NULL DEFAULT 0,
  `charge_amount` decimal(38,4) NOT NULL DEFAULT 0.0000,
  `point_amount` decimal(38,0) NOT NULL DEFAULT 0,
  `request_bytes` decimal(38,0) NOT NULL DEFAULT 0,
  `response_bytes` decimal(38,0) NOT NULL DEFAULT 0,
  `elapsed_sum` decimal(38,0) NOT NULL DEFAULT 0,
  `elapsed_min` bigint unsigned NOT NULL DEFAULT 0,
  `elapsed_max` bigint unsigned NOT NULL DEFAULT 0,
  `first_seen` datetime NOT NULL,
  `last_seen` datetime NOT NULL,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`bucket_start`, `interface_id`, `user_id`),
  KEY `idx_bp_call_api_metric_user_time` (`user_id`, `bucket_start`),
  KEY `idx_bp_call_api_metric_interface_time` (`interface_id`, `bucket_start`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='Per API and user call metrics by minute';

CREATE TABLE IF NOT EXISTS `bp_call_user_metric_minute` (
  `bucket_start` datetime NOT NULL,
  `user_id` bigint NOT NULL,
  `call_count` bigint unsigned NOT NULL DEFAULT 0,
  `success_count` bigint unsigned NOT NULL DEFAULT 0,
  `failure_count` bigint unsigned NOT NULL DEFAULT 0,
  `billable_count` bigint unsigned NOT NULL DEFAULT 0,
  `charge_amount` decimal(38,4) NOT NULL DEFAULT 0.0000,
  `point_amount` decimal(38,0) NOT NULL DEFAULT 0,
  `request_bytes` decimal(38,0) NOT NULL DEFAULT 0,
  `response_bytes` decimal(38,0) NOT NULL DEFAULT 0,
  `elapsed_sum` decimal(38,0) NOT NULL DEFAULT 0,
  `elapsed_min` bigint unsigned NOT NULL DEFAULT 0,
  `elapsed_max` bigint unsigned NOT NULL DEFAULT 0,
  `first_seen` datetime NOT NULL,
  `last_seen` datetime NOT NULL,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`bucket_start`, `user_id`),
  KEY `idx_bp_call_user_metric_user_time` (`user_id`, `bucket_start`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='Authoritative user contribution metrics by minute for current-account-aware totals';

CREATE TABLE IF NOT EXISTS `bp_call_region_ip_hour` (
  `bucket_start` datetime NOT NULL,
  `user_id` bigint NOT NULL,
  `country` varchar(64) NOT NULL DEFAULT '',
  `province` varchar(64) NOT NULL DEFAULT '',
  `city` varchar(64) NOT NULL DEFAULT '',
  `isp` varchar(128) NOT NULL DEFAULT '',
  `latitude` decimal(10,6) NOT NULL,
  `longitude` decimal(10,6) NOT NULL,
  `client_ip_hash` binary(32) NOT NULL COMMENT 'SHA-256 binary digest; no plaintext IP is retained',
  `call_count` bigint unsigned NOT NULL DEFAULT 0,
  `success_count` bigint unsigned NOT NULL DEFAULT 0,
  `failure_count` bigint unsigned NOT NULL DEFAULT 0,
  `request_bytes` decimal(38,0) NOT NULL DEFAULT 0,
  `response_bytes` decimal(38,0) NOT NULL DEFAULT 0,
  `elapsed_sum` decimal(38,0) NOT NULL DEFAULT 0,
  `first_seen` datetime NOT NULL,
  `last_seen` datetime NOT NULL,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`bucket_start`, `user_id`, `country`, `province`, `city`, `isp`, `latitude`, `longitude`, `client_ip_hash`),
  KEY `idx_bp_call_region_ip_time` (`bucket_start`, `country`, `province`, `city`),
  KEY `idx_bp_call_region_ip_user_time` (`user_id`, `bucket_start`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='Hourly region/IP-hash contributions supporting exact cross-bucket distinct-IP counts';

CREATE TABLE IF NOT EXISTS `bp_call_failure_ip_hour` (
  `bucket_start` datetime NOT NULL,
  `client_ip` varchar(64) NOT NULL COMMENT 'Administrator failure investigation requires the source IP',
  `user_id` bigint NOT NULL,
  `interface_id` bigint NOT NULL,
  `api_code` varchar(64) NOT NULL DEFAULT '',
  `country` varchar(64) NOT NULL DEFAULT '',
  `province` varchar(64) NOT NULL DEFAULT '',
  `city` varchar(64) NOT NULL DEFAULT '',
  `failure_count` bigint unsigned NOT NULL DEFAULT 0,
  `elapsed_sum` decimal(38,0) NOT NULL DEFAULT 0,
  `first_seen` datetime NOT NULL,
  `last_seen` datetime NOT NULL,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`bucket_start`, `client_ip`, `user_id`, `interface_id`),
  KEY `idx_bp_call_failure_ip_rank` (`bucket_start`, `failure_count`, `client_ip`),
  KEY `idx_bp_call_failure_ip_user_time` (`user_id`, `bucket_start`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='Hourly failed-call contributions by source IP, user and API';

CREATE TABLE IF NOT EXISTS `bp_call_failure_user_hour` (
  `bucket_start` datetime NOT NULL,
  `user_id` bigint NOT NULL,
  `interface_id` bigint NOT NULL,
  `api_code` varchar(64) NOT NULL DEFAULT '',
  `client_ip_hash` binary(32) NOT NULL,
  `has_client_ip` tinyint NOT NULL DEFAULT 0,
  `failure_count` bigint unsigned NOT NULL DEFAULT 0,
  `elapsed_sum` decimal(38,0) NOT NULL DEFAULT 0,
  `first_seen` datetime NOT NULL,
  `last_seen` datetime NOT NULL,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`bucket_start`, `user_id`, `interface_id`, `client_ip_hash`),
  KEY `idx_bp_call_failure_user_rank` (`bucket_start`, `failure_count`, `user_id`),
  KEY `idx_bp_call_failure_user_user_time` (`user_id`, `bucket_start`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='Hourly failed-call contributions by user, API and privacy-safe IP hash';

CREATE TABLE IF NOT EXISTS `bp_call_risk_fingerprint_5m` (
  `bucket_start` datetime NOT NULL,
  `interface_id` bigint NOT NULL,
  `request_fingerprint` char(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `user_id` bigint NOT NULL,
  `api_key_id` bigint NOT NULL DEFAULT 0,
  `client_ip_hash` binary(32) NOT NULL,
  `has_client_ip` tinyint NOT NULL DEFAULT 0,
  `client_ip` varchar(64) NOT NULL DEFAULT '',
  `api_code` varchar(64) NOT NULL DEFAULT '',
  `country` varchar(64) NOT NULL DEFAULT '',
  `province` varchar(64) NOT NULL DEFAULT '',
  `city` varchar(64) NOT NULL DEFAULT '',
  `call_count` bigint unsigned NOT NULL DEFAULT 0,
  `success_count` bigint unsigned NOT NULL DEFAULT 0,
  `failure_count` bigint unsigned NOT NULL DEFAULT 0,
  `elapsed_sum` decimal(38,0) NOT NULL DEFAULT 0,
  `first_seen` datetime NOT NULL,
  `last_seen` datetime NOT NULL,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`bucket_start`, `interface_id`, `request_fingerprint`, `user_id`, `api_key_id`, `client_ip_hash`),
  KEY `idx_bp_call_risk_single` (`bucket_start`, `client_ip_hash`, `interface_id`, `request_fingerprint`),
  KEY `idx_bp_call_risk_proxy` (`bucket_start`, `user_id`, `interface_id`, `request_fingerprint`),
  KEY `idx_bp_call_risk_distributed` (`bucket_start`, `interface_id`, `request_fingerprint`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='Five-minute exact contributors for single-IP, proxy-pool and distributed fingerprint rules';

CREATE TABLE IF NOT EXISTS `bp_analytics_checkpoint` (
  `worker_key` varchar(64) NOT NULL,
  `last_log_id` bigint unsigned NOT NULL DEFAULT 0,
  `last_log_created_at` datetime DEFAULT NULL,
  `source_rows` bigint unsigned NOT NULL DEFAULT 0,
  `contribution_rows` bigint unsigned NOT NULL DEFAULT 0,
  `last_batch_size` int unsigned NOT NULL DEFAULT 0,
  `last_started_at` datetime DEFAULT NULL,
  `last_completed_at` datetime DEFAULT NULL,
  `last_error` varchar(1000) NOT NULL DEFAULT '',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`worker_key`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='Idempotent high-water checkpoints for analytics workers';

INSERT IGNORE INTO `bp_analytics_checkpoint` (`worker_key`) VALUES ('call_log_rollup');


-- ============================================================
-- Migration: 000079_region_hour_rollup.up.sql
-- ============================================================

-- Compact regional traffic rollup for dashboards and map visualizations.
-- unique_ip_count is additive: each analytics source batch contributes the
-- exact distinct count of non-empty IPs inside that batch/hour/location/user
-- group. Summing it across batches or hours is an estimate and may count a
-- recurring IP more than once. The exact high-cardinality IP-hash rollup from
-- migration 000078 remains available for compatibility and exact audit needs.

CREATE TABLE IF NOT EXISTS `bp_call_region_hour` (
  `bucket_start` datetime NOT NULL,
  `user_id` bigint NOT NULL,
  `country` varchar(64) NOT NULL DEFAULT '',
  `province` varchar(64) NOT NULL DEFAULT '',
  `city` varchar(64) NOT NULL DEFAULT '',
  `isp` varchar(128) NOT NULL DEFAULT '',
  `latitude` decimal(10,6) NOT NULL,
  `longitude` decimal(10,6) NOT NULL,
  `call_count` bigint unsigned NOT NULL DEFAULT 0,
  `success_count` bigint unsigned NOT NULL DEFAULT 0,
  `failure_count` bigint unsigned NOT NULL DEFAULT 0,
  `unique_ip_count` bigint unsigned NOT NULL DEFAULT 0
    COMMENT 'Additive per-source-batch distinct non-empty IP estimate; not cross-window exact',
  `request_bytes` decimal(38,0) NOT NULL DEFAULT 0,
  `response_bytes` decimal(38,0) NOT NULL DEFAULT 0,
  `elapsed_sum` decimal(38,0) NOT NULL DEFAULT 0,
  `first_seen` datetime NOT NULL,
  `last_seen` datetime NOT NULL,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`bucket_start`, `user_id`, `country`, `province`, `city`, `isp`, `latitude`, `longitude`),
  KEY `idx_bp_call_region_hour_time` (`bucket_start`, `country`, `province`, `city`),
  KEY `idx_bp_call_region_hour_user_time` (`user_id`, `bucket_start`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='Compact hourly regional traffic; unique_ip_count is an additive estimate';

-- The 30-day administrator atlas is the widest reader. Seed one extra day so
-- an upgraded 78 database is immediately useful even though its analytics
-- checkpoint has already passed historical raw logs. This bounded backfill
-- reads the retained exact 000078 table instead of the much larger raw log.
INSERT INTO `bp_call_region_hour` (
  `bucket_start`,`user_id`,`country`,`province`,`city`,`isp`,`latitude`,`longitude`,
  `call_count`,`success_count`,`failure_count`,`unique_ip_count`,
  `request_bytes`,`response_bytes`,`elapsed_sum`,`first_seen`,`last_seen`
)
SELECT
  `bucket_start`,`user_id`,`country`,`province`,`city`,`isp`,`latitude`,`longitude`,
  SUM(`call_count`),SUM(`success_count`),SUM(`failure_count`),
  COUNT(DISTINCT CASE
    WHEN `client_ip_hash` <> UNHEX(SHA2('bugpk-region-ip-v1:',256)) THEN `client_ip_hash`
  END),SUM(`request_bytes`),SUM(`response_bytes`),
  SUM(`elapsed_sum`),MIN(`first_seen`),MAX(`last_seen`)
FROM `bp_call_region_ip_hour` FORCE INDEX (`idx_bp_call_region_ip_time`)
WHERE `bucket_start` >= DATE_FORMAT(DATE_SUB(NOW(), INTERVAL 31 DAY),'%Y-%m-%d %H:00:00')
GROUP BY `bucket_start`,`user_id`,`country`,`province`,`city`,`isp`,`latitude`,`longitude`
ON DUPLICATE KEY UPDATE `bucket_start`=VALUES(`bucket_start`);


-- ============================================================
-- Migration: 000080_admin_overview_revenue_index.up.sql
-- ============================================================

-- A single-row revenue snapshot keeps dashboard reads independent of the
-- append-only billing-record cardinality. The analytics worker advances this
-- value in the same transaction as its source checkpoint.
CREATE TABLE IF NOT EXISTS `bp_billing_revenue_total` (
  `id` tinyint unsigned NOT NULL,
  `confirmed_charge_amount` decimal(38,4) NOT NULL DEFAULT 0.0000,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='Constant-cardinality confirmed revenue snapshot for dashboards';

INSERT INTO `bp_billing_revenue_total` (`id`, `confirmed_charge_amount`)
SELECT 1, COALESCE(SUM(`charge_amount`), 0)
FROM `bp_billing_record`
WHERE `status` = 'CONFIRMED'
ON DUPLICATE KEY UPDATE
  `confirmed_charge_amount` = VALUES(`confirmed_charge_amount`);


-- ============================================================
-- Migration: 000081_call_log_list_index.up.sql
-- ============================================================

ALTER TABLE `bp_interface_call_log`
  ADD KEY `idx_bp_call_log_interface_success_created_id` (
    `interface_id`,
    `success`,
    `created_at`,
    `id`
  );


-- ============================================================
-- Migration: 000082_ticket_statistics_indexes.up.sql
-- ============================================================

ALTER TABLE `bp_ticket`
  ADD KEY `idx_bp_ticket_created_at` (`created_at`),
  ADD KEY `idx_bp_ticket_resolved_at` (`resolved_at`);


-- ============================================================
-- Migration: 000083_slow_query_optimization.up.sql
-- ============================================================

-- Slow-query optimization: cover the two remaining high-frequency access
-- patterns that were still scanning historical rows without an index that
-- matches their WHERE + ORDER BY shape.

-- 1) Call-log list/count filtered by (user_id, user_visible) and ordered by
--    created_at. The previous idx_bp_interface_call_log_user (user_id,
--    created_at) still had to read the clustered row to evaluate user_visible
--    and could not serve the "my logs" paging path as an index-only scan.
--    (id) is the InnoDB implicit primary-key suffix of every secondary index,
--    so (user_id, user_visible, created_at) also satisfies ORDER BY
--    created_at, id.
ALTER TABLE `bp_interface_call_log`
  ADD KEY `idx_bp_call_log_user_visible` (`user_id`, `user_visible`, `created_at`);

-- 2) Confirmed-revenue materialization sums charge_amount for every row with
--    status='CONFIRMED'. A covering (status, charge_amount) index turns that
--    backfill into an index-only scan instead of a clustered fetch per row.
--    The steady-state dashboard already reads bp_billing_revenue_total; this
--    index only protects the one-time/backfill SELECT SUM(...) path.
ALTER TABLE `bp_billing_record`
  ADD KEY `idx_bp_billing_record_status_amount` (`status`, `charge_amount`);


-- ============================================================
-- Migration: 000084_call_log_user_interface_index.up.sql
-- ============================================================

-- User + interface + time composite for the user-scoped call-log list path
-- ("my logs" filtered by one interface, ordered by created_at). The existing
-- idx_bp_call_log_user_visible (user_id, user_visible, created_at) must
-- post-filter interface_id from row lookups for users whose traffic spans
-- many interfaces; this index narrows first. The InnoDB implicit primary-key
-- suffix keeps ORDER BY created_at, id seekable for cursor pagination.
ALTER TABLE `bp_interface_call_log`
  ADD KEY `idx_bp_call_log_user_interface` (`user_id`, `interface_id`, `created_at`);


-- ============================================================
-- Migration: 000085_call_log_verified_indexes.up.sql
-- ============================================================

-- Verify-index migration: add the two composite indexes whose query shapes
-- were validated with EXPLAIN ANALYZE on million-row synthetic data but are
-- still not covered by any existing key:
--
--   1) (client_ip, created_at)      — "filter logs by one client IP, ordered
--      by created_at". Previously every such query fell back to a full
--      clustered scan (the region indexes lead with ip_* dimensions, not
--      client_ip). The InnoDB implicit id suffix keeps ORDER BY created_at,
--      id seekable for keyset pagination.
--
--   2) (interface_id, api_key_id, created_at) — "filter logs by one
--      interface AND one API key", a recurring admin diagnostic shape that
--      old indexes could only serve by post-filtering one side from row
--      lookups. Leading with both equality columns narrows first, and the
--      implicit id suffix again covers ORDER BY created_at, id.
--
-- Deliberately NOT added (verified already covered or low-selectivity):
--   - trace_id: single-column idx_bp_interface_call_log_trace exists and
--     point lookups are already seekable; a (trace_id, created_at) suffix
--     only helps ORDER BY on multi-row trace results, measured negligible.
--   - api_key_id: idx_bp_interface_call_log_key (api_key_id, created_at)
--     already exists since 000004.
--   - api_code: idx_bp_interface_call_log_api_code (api_code, created_at)
--     already exists since 000004.
--   - user_id + user_visible + created_at: idx_bp_call_log_user_visible
--     already exists since 000083.
ALTER TABLE `bp_interface_call_log`
  ADD KEY `idx_bp_call_log_client_ip_created` (`client_ip`, `created_at`),
  ADD KEY `idx_bp_call_log_interface_key_created` (`interface_id`, `api_key_id`, `created_at`);


-- ============================================================
-- Migration: 000086_hupijiao_payment.up.sql
-- ============================================================

CREATE TABLE IF NOT EXISTS `bp_payment_hupijiao_config` (
  `method` varchar(16) NOT NULL COMMENT 'ALIPAY/WECHAT',
  `enabled` tinyint NOT NULL DEFAULT 0,
  `app_id` varchar(128) NOT NULL DEFAULT '',
  `app_secret_cipher` text,
  `create_url` varchar(512) NOT NULL DEFAULT 'https://api.xunhupay.com/payment/do.html',
  `query_url` varchar(512) NOT NULL DEFAULT 'https://api.xunhupay.com/payment/query.html',
  `notify_url` varchar(512) DEFAULT NULL,
  `return_url` varchar(512) DEFAULT NULL,
  `callback_url` varchar(512) DEFAULT NULL,
  `wap_url` varchar(512) DEFAULT NULL,
  `wap_name` varchar(128) DEFAULT NULL,
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`method`),
  CONSTRAINT `chk_bp_hupijiao_method` CHECK (`method` IN ('ALIPAY','WECHAT'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='Hupijiao payment channel config';

INSERT INTO `bp_payment_hupijiao_config` (`method`, `enabled`)
VALUES ('ALIPAY', 0), ('WECHAT', 0)
ON DUPLICATE KEY UPDATE `method` = VALUES(`method`);

CREATE TABLE IF NOT EXISTS `bp_payment_method` (
  `method_code` varchar(32) NOT NULL,
  `enabled` tinyint NOT NULL DEFAULT 0,
  `sort_order` int NOT NULL DEFAULT 0,
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`method_code`),
  KEY `idx_bp_payment_method_enabled_sort` (`enabled`, `sort_order`, `method_code`),
  CONSTRAINT `chk_bp_payment_method_code` CHECK (`method_code` IN ('BALANCE','ALIPAY','WECHAT','HUPIJIAO_ALIPAY','HUPIJIAO_WECHAT'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='Checkout payment methods';

INSERT INTO `bp_payment_method` (`method_code`, `enabled`, `sort_order`)
VALUES
  ('BALANCE', 1, 10),
  ('ALIPAY', 1, 20),
  ('WECHAT', 0, 30),
  ('HUPIJIAO_ALIPAY', 0, 40),
  ('HUPIJIAO_WECHAT', 0, 50)
ON DUPLICATE KEY UPDATE `method_code` = VALUES(`method_code`);

SET @bugpk_payment_order_backfill_max_id = (SELECT COALESCE(MAX(`id`), 0) FROM `bp_payment_order`);

ALTER TABLE `bp_payment_order`
  ADD COLUMN `pay_channel` varchar(32) NOT NULL DEFAULT 'ALIPAY' COMMENT 'ALIPAY/WECHAT/HUPIJIAO_ALIPAY/HUPIJIAO_WECHAT' AFTER `product`,
  ADD KEY `idx_bp_payment_order_channel_status` (`pay_channel`, `status`, `created_at`);

UPDATE `bp_payment_order`
SET `pay_channel` = CASE
  WHEN `trade_type` IN ('NATIVE','H5','JSAPI','APP','MINI') THEN 'WECHAT'
  ELSE 'ALIPAY'
END
WHERE `id` <= @bugpk_payment_order_backfill_max_id
  AND `pay_channel` = 'ALIPAY';

INSERT INTO `bp_menu`
  (`id`, `pid`, `name`, `path`, `component`, `redirect`, `type`, `auth_code`, `meta`, `status`)
VALUES
  (112, 101, 'HupijiaoConfigUpdate', '', NULL, NULL, 'button', 'payment:hupijiao:update', JSON_OBJECT('title', 'Update Hupijiao Config'), 1),
  (113, 101, 'PaymentMethodUpdate', '', NULL, NULL, 'button', 'payment:method:update', JSON_OBJECT('title', 'Update Payment Methods'), 1)
ON DUPLICATE KEY UPDATE
  `pid` = VALUES(`pid`),
  `auth_code` = VALUES(`auth_code`),
  `meta` = VALUES(`meta`),
  `status` = VALUES(`status`);

INSERT IGNORE INTO `bp_role_menu` (`role_id`, `menu_id`)
SELECT r.`id`, m.`id`
FROM `bp_role` r
CROSS JOIN `bp_menu` m
WHERE r.`role_key` = 'admin'
  AND m.`id` IN (112, 113);


-- ============================================================
-- Migration: 000087_polling_stop_code.up.sql
-- ============================================================

-- ============================================================
-- 000087: interface polling stop code (configurable stop-polling code)
-- Adds bp_interface_api.polling_stop_code: when a node's response
-- body `code` field (default field, see polling_check_field) equals
-- this configured value, the gateway stops polling and returns that
-- node's response directly (Success=false, not counted as a health
-- failure, no cooldown).
-- Empty (NULL / '') keeps the default behavior: only an upstream
-- HTTP 404 terminates polling immediately; all other non-2xx / network
-- errors keep trying every remaining candidate.
-- ============================================================

ALTER TABLE `bp_interface_api`
  ADD COLUMN `polling_stop_code` varchar(255) DEFAULT NULL
    COMMENT '终止轮询业务码：响应体polling_check_field(默认code)等于该值时终止轮询并透传；空=保留HTTP404终止兜底' AFTER `polling_check_expected`;

-- Reassert the aggregate failure-log capacity for installations whose schema
-- version reached 73+ but whose column definition drifted back to VARCHAR.
ALTER TABLE `bp_interface_call_log`
  MODIFY COLUMN `upstream_url` TEXT DEFAULT NULL COMMENT '上游节点引用（不可逆脱敏，多节点）';


-- ============================================================
-- Migration: 000088_risk_control_overview_snapshot.up.sql
-- ============================================================

-- Persist the expensive fixed 24-hour risk-control overview. The HTTP path
-- reads one constant-cardinality row while a serial background worker refreshes
-- the payload from the exact rollups.
CREATE TABLE IF NOT EXISTS `bp_risk_control_overview_snapshot` (
  `window_minutes` smallint unsigned NOT NULL,
  `payload` json NOT NULL,
  `source_updated_at` datetime(6) NOT NULL,
  `created_at` datetime(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  `updated_at` datetime(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
  PRIMARY KEY (`window_minutes`),
  CONSTRAINT `chk_bp_risk_overview_window` CHECK (`window_minutes` > 0 AND `window_minutes` <= 1440)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='Persisted fixed-window risk-control dashboard snapshots';

-- Candidate detection reads only these key columns plus has_client_ip and
-- call_count. Extending the existing rule-specific indexes makes the range
-- scan covering and avoids one clustered-row lookup per contributor.
ALTER TABLE `bp_call_risk_fingerprint_5m`
  DROP INDEX `idx_bp_call_risk_single`,
  DROP INDEX `idx_bp_call_risk_proxy`,
  DROP INDEX `idx_bp_call_risk_distributed`,
  ADD INDEX `idx_bp_call_risk_single` (`bucket_start`, `client_ip_hash`, `interface_id`, `request_fingerprint`, `has_client_ip`, `call_count`),
  ADD INDEX `idx_bp_call_risk_proxy` (`bucket_start`, `user_id`, `interface_id`, `request_fingerprint`, `has_client_ip`, `client_ip_hash`, `call_count`),
  ADD INDEX `idx_bp_call_risk_distributed` (`bucket_start`, `interface_id`, `request_fingerprint`, `has_client_ip`, `client_ip_hash`, `user_id`, `call_count`);


-- ============================================================
-- Migration: 000089_home_overview_performance.up.sql
-- ============================================================

-- Reduce the cardinality of the public homepage hot-API reader.
-- Minute rollups remain the source for short-window charts and audit views;
-- this table is the 30-day interface ranking projection.
CREATE TABLE IF NOT EXISTS `bp_call_api_metric_day` (
  `bucket_date` date NOT NULL,
  `interface_id` bigint NOT NULL,
  `user_id` bigint NOT NULL,
  `api_code` varchar(64) NOT NULL DEFAULT '',
  `call_count` bigint unsigned NOT NULL DEFAULT 0,
  `success_count` bigint unsigned NOT NULL DEFAULT 0,
  `failure_count` bigint unsigned NOT NULL DEFAULT 0,
  `elapsed_sum` decimal(38,0) NOT NULL DEFAULT 0,
  `first_seen` datetime NOT NULL,
  `last_seen` datetime NOT NULL,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`bucket_date`, `interface_id`, `user_id`),
  KEY `idx_bp_call_api_day_interface_date` (`interface_id`, `bucket_date`),
  KEY `idx_bp_call_api_day_user_date` (`user_id`, `bucket_date`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='Per API and user daily call metrics for bounded 30-day rankings';

-- Backfill from the compact minute projection instead of the append-only log.
-- The range is intentionally one day wider than the public 30-day window so
-- an upgrade at a day boundary does not produce an empty first ranking.
INSERT INTO `bp_call_api_metric_day` (
  `bucket_date`,`interface_id`,`user_id`,`api_code`,`call_count`,
  `success_count`,`failure_count`,`elapsed_sum`,`first_seen`,`last_seen`
)
SELECT
  DATE(`bucket_start`),`interface_id`,`user_id`,MAX(`api_code`),SUM(`call_count`),
  SUM(`success_count`),SUM(`failure_count`),SUM(`elapsed_sum`),
  MIN(`first_seen`),MAX(`last_seen`)
FROM `bp_call_api_metric_minute` FORCE INDEX (`PRIMARY`)
WHERE `bucket_start` >= DATE_SUB(CURRENT_DATE(), INTERVAL 31 DAY)
GROUP BY DATE(`bucket_start`),`interface_id`,`user_id`
ON DUPLICATE KEY UPDATE
  `api_code`=VALUES(`api_code`),
  `call_count`=VALUES(`call_count`),
  `success_count`=VALUES(`success_count`),
  `failure_count`=VALUES(`failure_count`),
  `elapsed_sum`=VALUES(`elapsed_sum`),
  `first_seen`=VALUES(`first_seen`),
  `last_seen`=VALUES(`last_seen`);

-- The HTTP path reads this constant-cardinality row. The NULL source time
-- marks the seeded row as not ready; the first background refresh replaces it
-- with a measured snapshot. This avoids returning a misleading zero snapshot
-- during startup while still preventing an unbounded request-time scan.
CREATE TABLE IF NOT EXISTS `bp_home_overview_snapshot` (
  `id` tinyint unsigned NOT NULL,
  `payload` json NOT NULL,
  `source_updated_at` datetime(6) DEFAULT NULL,
  `created_at` datetime(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  `updated_at` datetime(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
  PRIMARY KEY (`id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='Persisted public homepage analytics snapshot';

INSERT INTO `bp_home_overview_snapshot` (`id`,`payload`,`source_updated_at`)
VALUES (
  1,
  JSON_OBJECT(
    'stats',JSON_OBJECT(
      'activeRegions24h',0,
      'activeRegions24hDelta',0,
      'apiTotal',0,
      'availabilityPercent','0',
      'availabilitySource','CALL_SUCCESS',
      'calls24h',0,
      'calls24hDeltaPercent','0',
      'enabledApiTotal',0,
      'peakQps24h',0,
      'peakQps24hDelta',0,
      'userTotal',0
    ),
    'hotApis',JSON_ARRAY()
  ),
  NULL
)
ON DUPLICATE KEY UPDATE `id`=VALUES(`id`);


-- ============================================================
-- Migration: 000090_analytics_rollup_repair.up.sql
-- ============================================================

-- 000090: persist a bounded analytics projection repair request.
--
-- Migrations 000078, 000079 and 000089 create/read rollup projections, but
-- they cannot tell whether an older installation already advanced its
-- checkpoint while only a subset of those projections was populated.  Mark
-- one bounded repair task here; the Go worker performs the actual rebuild
-- outside the migration transaction.  Keeping the migration DDL/data-only
-- makes upgrades safe for very large append-only call-log tables.

SET @bugpk_analytics_repair_schema = DATABASE();

SET @bugpk_analytics_repair_sql = IF(EXISTS(
  SELECT 1
  FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_analytics_repair_schema
    AND TABLE_NAME = 'bp_analytics_checkpoint'
    AND COLUMN_NAME = 'repair_status'
), 'SELECT 1 WHERE 0',
  'ALTER TABLE `bp_analytics_checkpoint` ADD COLUMN `repair_status` varchar(16) NOT NULL DEFAULT ''NONE'' COMMENT ''Bounded rollup repair state'' AFTER `last_error`');
PREPARE bugpk_analytics_repair_stmt FROM @bugpk_analytics_repair_sql;
EXECUTE bugpk_analytics_repair_stmt;
DEALLOCATE PREPARE bugpk_analytics_repair_stmt;

SET @bugpk_analytics_repair_sql = IF(EXISTS(
  SELECT 1
  FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_analytics_repair_schema
    AND TABLE_NAME = 'bp_analytics_checkpoint'
    AND COLUMN_NAME = 'repair_window_days'
), 'SELECT 1 WHERE 0',
  'ALTER TABLE `bp_analytics_checkpoint` ADD COLUMN `repair_window_days` smallint unsigned NOT NULL DEFAULT 0 COMMENT ''Bounded repair window in days'' AFTER `repair_status`');
PREPARE bugpk_analytics_repair_stmt FROM @bugpk_analytics_repair_sql;
EXECUTE bugpk_analytics_repair_stmt;
DEALLOCATE PREPARE bugpk_analytics_repair_stmt;

SET @bugpk_analytics_repair_sql = IF(EXISTS(
  SELECT 1
  FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_analytics_repair_schema
    AND TABLE_NAME = 'bp_analytics_checkpoint'
    AND COLUMN_NAME = 'repair_requested_at'
), 'SELECT 1 WHERE 0',
  'ALTER TABLE `bp_analytics_checkpoint` ADD COLUMN `repair_requested_at` datetime(6) DEFAULT NULL COMMENT ''Time the bounded repair was requested'' AFTER `repair_window_days`');
PREPARE bugpk_analytics_repair_stmt FROM @bugpk_analytics_repair_sql;
EXECUTE bugpk_analytics_repair_stmt;
DEALLOCATE PREPARE bugpk_analytics_repair_stmt;

SET @bugpk_analytics_repair_sql = IF(EXISTS(
  SELECT 1
  FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_analytics_repair_schema
    AND TABLE_NAME = 'bp_analytics_checkpoint'
    AND COLUMN_NAME = 'repair_started_at'
), 'SELECT 1 WHERE 0',
  'ALTER TABLE `bp_analytics_checkpoint` ADD COLUMN `repair_started_at` datetime(6) DEFAULT NULL COMMENT ''Time the bounded repair started'' AFTER `repair_requested_at`');
PREPARE bugpk_analytics_repair_stmt FROM @bugpk_analytics_repair_sql;
EXECUTE bugpk_analytics_repair_stmt;
DEALLOCATE PREPARE bugpk_analytics_repair_stmt;

SET @bugpk_analytics_repair_sql = IF(EXISTS(
  SELECT 1
  FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_analytics_repair_schema
    AND TABLE_NAME = 'bp_analytics_checkpoint'
    AND COLUMN_NAME = 'repair_completed_at'
), 'SELECT 1 WHERE 0',
  'ALTER TABLE `bp_analytics_checkpoint` ADD COLUMN `repair_completed_at` datetime(6) DEFAULT NULL COMMENT ''Time the bounded repair completed'' AFTER `repair_started_at`');
PREPARE bugpk_analytics_repair_stmt FROM @bugpk_analytics_repair_sql;
EXECUTE bugpk_analytics_repair_stmt;
DEALLOCATE PREPARE bugpk_analytics_repair_stmt;

INSERT IGNORE INTO `bp_analytics_checkpoint` (`worker_key`)
VALUES ('call_log_rollup');

-- Request the repair exactly once for this migration.  A completed request is
-- preserved if an operator re-runs the standalone upgrade SQL; pending/running
-- requests remain retryable and never reset the source checkpoint.
-- The worker interprets the default as a bounded `INTERVAL 31 DAY` window.
UPDATE `bp_analytics_checkpoint`
SET `repair_status` = IF(`repair_status` = 'COMPLETE', 'COMPLETE', 'PENDING'),
    `repair_window_days` = IF(`repair_window_days` = 0, 31, `repair_window_days`),
    `repair_requested_at` = IF(
      `repair_status` = 'COMPLETE',
      `repair_requested_at`,
      COALESCE(`repair_requested_at`, NOW(6))
    ),
    `repair_started_at` = IF(`repair_status` = 'COMPLETE', `repair_started_at`, NULL),
    `repair_completed_at` = IF(`repair_status` = 'COMPLETE', `repair_completed_at`, NULL)
WHERE `worker_key` = 'call_log_rollup';


-- ============================================================
-- Migration: 000091_interface_request_timeout.up.sql
-- ============================================================

-- Configurable upstream request timeout.
-- Interface rows use request_timeout_ms=0 as the inherit marker; when
-- request_timeout_override=1 a valid non-zero value takes precedence over the
-- singleton global policy row.

SET @bugpk_schema = DATABASE();

SET @bugpk_add_interface_timeout = IF(EXISTS(
  SELECT 1 FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_schema
    AND TABLE_NAME = 'bp_interface_api'
    AND COLUMN_NAME = 'request_timeout_ms'
), 0, 1);
SET @bugpk_sql = IF(@bugpk_add_interface_timeout = 1,
  'ALTER TABLE `bp_interface_api` ADD COLUMN `request_timeout_ms` int NOT NULL DEFAULT 0 COMMENT ''Upstream timeout in milliseconds; 0 inherits global'' AFTER `delivery_policy_version`',
  'SELECT 1 WHERE 0');
PREPARE bugpk_stmt FROM @bugpk_sql;
EXECUTE bugpk_stmt;
DEALLOCATE PREPARE bugpk_stmt;

SET @bugpk_add_interface_timeout_override = IF(EXISTS(
  SELECT 1 FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_schema
    AND TABLE_NAME = 'bp_interface_api'
    AND COLUMN_NAME = 'request_timeout_override'
), 0, 1);
SET @bugpk_sql = IF(@bugpk_add_interface_timeout_override = 1,
  'ALTER TABLE `bp_interface_api` ADD COLUMN `request_timeout_override` tinyint NOT NULL DEFAULT 0 COMMENT ''1=use interface timeout, 0=inherit global'' AFTER `request_timeout_ms`',
  'SELECT 1 WHERE 0');
PREPARE bugpk_stmt FROM @bugpk_sql;
EXECUTE bugpk_stmt;
DEALLOCATE PREPARE bugpk_stmt;

SET @bugpk_add_global_timeout = IF(EXISTS(
  SELECT 1 FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_schema
    AND TABLE_NAME = 'bp_interface_global_delivery_policy'
    AND COLUMN_NAME = 'request_timeout_ms'
), 0, 1);
SET @bugpk_sql = IF(@bugpk_add_global_timeout = 1,
  'ALTER TABLE `bp_interface_global_delivery_policy` ADD COLUMN `request_timeout_ms` int NOT NULL DEFAULT 15000 COMMENT ''Default upstream timeout in milliseconds'' AFTER `cache_rules`',
  'SELECT 1 WHERE 0');
PREPARE bugpk_stmt FROM @bugpk_sql;
EXECUTE bugpk_stmt;
DEALLOCATE PREPARE bugpk_stmt;

-- Do not overwrite a value an administrator already chose. This only repairs
-- a newly added/legacy zero value and is safe to repeat.
SET @bugpk_sql = IF(@bugpk_add_global_timeout = 1,
  'UPDATE `bp_interface_global_delivery_policy` SET `request_timeout_ms` = 15000 WHERE `id` = 1 AND (`request_timeout_ms` IS NULL OR `request_timeout_ms` <= 0)',
  'SELECT 1 WHERE 0');
PREPARE bugpk_stmt FROM @bugpk_sql;
EXECUTE bugpk_stmt;
DEALLOCATE PREPARE bugpk_stmt;

INSERT INTO `bp_interface_global_delivery_policy`
  (`id`, `request_timeout_ms`)
VALUES (1, 15000)
ON DUPLICATE KEY UPDATE `id` = VALUES(`id`);


-- ============================================================
-- Migration: 000092_analytics_rollup_repair_cursor.up.sql
-- ============================================================

-- 000092: make analytics projection repair resumable and transaction-bounded.
--
-- This migration only persists repair metadata. The serial Go analytics
-- worker clears and rebuilds the bounded window in small committed steps, so
-- a large installation can resume after a timeout or process restart without
-- repeating already committed source chunks.

SET @bugpk_analytics_cursor_schema = DATABASE();

SET @bugpk_analytics_cursor_sql = IF(EXISTS(
  SELECT 1 FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_analytics_cursor_schema
    AND TABLE_NAME = 'bp_analytics_checkpoint'
    AND COLUMN_NAME = 'repair_upper_log_id'
), 'SELECT 1 WHERE 0',
  'ALTER TABLE `bp_analytics_checkpoint` ADD COLUMN `repair_upper_log_id` bigint unsigned NOT NULL DEFAULT 0 COMMENT ''Stable source high-water mark for resumable repair'' AFTER `repair_completed_at`');
PREPARE bugpk_analytics_cursor_stmt FROM @bugpk_analytics_cursor_sql;
EXECUTE bugpk_analytics_cursor_stmt;
DEALLOCATE PREPARE bugpk_analytics_cursor_stmt;

SET @bugpk_analytics_cursor_sql = IF(EXISTS(
  SELECT 1 FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_analytics_cursor_schema
    AND TABLE_NAME = 'bp_analytics_checkpoint'
    AND COLUMN_NAME = 'repair_cursor_id'
), 'SELECT 1 WHERE 0',
  'ALTER TABLE `bp_analytics_checkpoint` ADD COLUMN `repair_cursor_id` bigint unsigned NOT NULL DEFAULT 0 COMMENT ''Last source id committed by resumable repair'' AFTER `repair_upper_log_id`');
PREPARE bugpk_analytics_cursor_stmt FROM @bugpk_analytics_cursor_sql;
EXECUTE bugpk_analytics_cursor_stmt;
DEALLOCATE PREPARE bugpk_analytics_cursor_stmt;

SET @bugpk_analytics_cursor_sql = IF(EXISTS(
  SELECT 1 FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_analytics_cursor_schema
    AND TABLE_NAME = 'bp_analytics_checkpoint'
    AND COLUMN_NAME = 'repair_projection_mask'
), 'SELECT 1 WHERE 0',
  'ALTER TABLE `bp_analytics_checkpoint` ADD COLUMN `repair_projection_mask` smallint unsigned NOT NULL DEFAULT 0 COMMENT ''Bit mask of projections rebuilt by current repair'' AFTER `repair_cursor_id`');
PREPARE bugpk_analytics_cursor_stmt FROM @bugpk_analytics_cursor_sql;
EXECUTE bugpk_analytics_cursor_stmt;
DEALLOCATE PREPARE bugpk_analytics_cursor_stmt;

SET @bugpk_analytics_cursor_sql = IF(EXISTS(
  SELECT 1 FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_analytics_cursor_schema
    AND TABLE_NAME = 'bp_analytics_checkpoint'
    AND COLUMN_NAME = 'repair_phase'
), 'SELECT 1 WHERE 0',
  'ALTER TABLE `bp_analytics_checkpoint` ADD COLUMN `repair_phase` varchar(16) NOT NULL DEFAULT ''IDLE'' COMMENT ''IDLE/CLEAR/ROLLUP/FINALIZE'' AFTER `repair_projection_mask`');
PREPARE bugpk_analytics_cursor_stmt FROM @bugpk_analytics_cursor_sql;
EXECUTE bugpk_analytics_cursor_stmt;
DEALLOCATE PREPARE bugpk_analytics_cursor_stmt;

SET @bugpk_analytics_cursor_sql = IF(EXISTS(
  SELECT 1 FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_analytics_cursor_schema
    AND TABLE_NAME = 'bp_analytics_checkpoint'
    AND COLUMN_NAME = 'repair_window_start'
), 'SELECT 1 WHERE 0',
  'ALTER TABLE `bp_analytics_checkpoint` ADD COLUMN `repair_window_start` datetime(6) DEFAULT NULL COMMENT ''Stable inclusive time boundary for current repair'' AFTER `repair_phase`');
PREPARE bugpk_analytics_cursor_stmt FROM @bugpk_analytics_cursor_sql;
EXECUTE bugpk_analytics_cursor_stmt;
DEALLOCATE PREPARE bugpk_analytics_cursor_stmt;

SET @bugpk_analytics_cursor_sql = IF(EXISTS(
  SELECT 1 FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_analytics_cursor_schema
    AND TABLE_NAME = 'bp_analytics_checkpoint'
    AND COLUMN_NAME = 'repair_clear_projection'
), 'SELECT 1 WHERE 0',
  'ALTER TABLE `bp_analytics_checkpoint` ADD COLUMN `repair_clear_projection` tinyint unsigned NOT NULL DEFAULT 0 COMMENT ''Next projection index to clear in bounded batches'' AFTER `repair_window_start`');
PREPARE bugpk_analytics_cursor_stmt FROM @bugpk_analytics_cursor_sql;
EXECUTE bugpk_analytics_cursor_stmt;
DEALLOCATE PREPARE bugpk_analytics_cursor_stmt;

INSERT IGNORE INTO `bp_analytics_checkpoint` (`worker_key`)
VALUES ('call_log_rollup');

-- repair_projection_mask=0 is the one-time migration marker. Keeping the mask
-- after completion makes this standalone SQL safe to rerun without scheduling
-- another destructive clear/rebuild cycle.
UPDATE `bp_analytics_checkpoint`
SET `repair_status` = 'PENDING',
    `repair_window_days` = IF(`repair_window_days` = 0, 31, `repair_window_days`),
    `repair_requested_at` = NOW(6),
    `repair_started_at` = NULL,
    `repair_completed_at` = NULL,
    `repair_upper_log_id` = 0,
    `repair_cursor_id` = 0,
    `repair_projection_mask` = 511,
    `repair_phase` = 'CLEAR',
    `repair_window_start` = NULL,
    `repair_clear_projection` = 0
WHERE `worker_key` = 'call_log_rollup'
  AND `repair_projection_mask` = 0;


-- ============================================================
-- Migration: 000093_site_global_loading_preset.up.sql
-- ============================================================

-- 000093: add the global loading animation preset to the site config.
--
-- The startup splash (loading_animation_preset) and the in-app loading
-- indicator serve different moments, so they are configured independently:
-- the splash runs before the bundle executes, while this preset drives route
-- switches and pending requests. Default 'bar' keeps content visible, which
-- shortens perceived waiting compared with a blocking overlay.

SET @bugpk_global_loading_schema = DATABASE();

SET @bugpk_global_loading_sql = IF(EXISTS(
  SELECT 1 FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_global_loading_schema
    AND TABLE_NAME = 'bp_site_config'
    AND COLUMN_NAME = 'global_loading_preset'
), 'SELECT 1 WHERE 0',
  'ALTER TABLE `bp_site_config` ADD COLUMN `global_loading_preset` varchar(32) NOT NULL DEFAULT ''bar'' COMMENT ''Global loading animation: bar/ring/skeleton/packet/logo/dots'' AFTER `loading_animation_url`');
PREPARE bugpk_global_loading_stmt FROM @bugpk_global_loading_sql;
EXECUTE bugpk_global_loading_stmt;
DEALLOCATE PREPARE bugpk_global_loading_stmt;

-- Normalise any value outside the supported set back to the default so the
-- API never returns a preset the frontend cannot render.
UPDATE `bp_site_config`
SET `global_loading_preset` = 'bar'
WHERE `global_loading_preset` NOT IN (
  'bar', 'ring', 'skeleton', 'packet', 'logo', 'dots'
);

-- The splash gained forge/scan/rail/dots in this release. Legacy rows still
-- holding pulse/bars keep rendering (both remain accepted), but a row created
-- before the preset column existed may hold an empty string.
UPDATE `bp_site_config`
SET `loading_animation_preset` = 'forge'
WHERE `loading_animation_preset` = ''
   OR `loading_animation_preset` IS NULL;


-- ============================================================
-- Migration: 000094_interface_split_shared_user_qps.up.sql
-- ============================================================

-- 000094: split the single interface QPS bucket into two independent limits.
--
-- Previously bp_interface_api had one `qps_limit` column that doubled as the
-- per-user cap and the shared-traffic ceiling.  This migration introduces a
-- dedicated `user_qps_limit` (per-user QPS) and renames the original column to
-- `shared_qps_limit` (global interface capacity), resetting the latter to 0 so
-- the runtime can treat 0 as "unlimited" until operators set an explicit value.
-- Existing qps_limit values are preserved by copying them into user_qps_limit
-- before the rename, maintaining backward-compatible per-user behaviour.

SET @bugpk_qps_schema = DATABASE();

-- ---------------------------------------------------------------------------
-- Step 1: add `user_qps_limit` AFTER `qps_limit` (idempotent).
-- ---------------------------------------------------------------------------
SET @bugpk_qps_sql = IF(EXISTS(
  SELECT 1 FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_qps_schema
    AND TABLE_NAME = 'bp_interface_api'
    AND COLUMN_NAME = 'user_qps_limit'
), 'SELECT 1 WHERE 0',
  'ALTER TABLE `bp_interface_api` ADD COLUMN `user_qps_limit` int NOT NULL DEFAULT 0 COMMENT ''每个用户调用该接口的最高 QPS；0 表示不限制'' AFTER `qps_limit`');
PREPARE bugpk_qps_stmt FROM @bugpk_qps_sql;
EXECUTE bugpk_qps_stmt;
DEALLOCATE PREPARE bugpk_qps_stmt;

-- ---------------------------------------------------------------------------
-- Step 2: copy old qps_limit values into user_qps_limit (only while the
-- original column still exists — before the rename).
-- ---------------------------------------------------------------------------
SET @bugpk_qps_sql = IF(EXISTS(
  SELECT 1 FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_qps_schema
    AND TABLE_NAME = 'bp_interface_api'
    AND COLUMN_NAME = 'qps_limit'
), 'UPDATE `bp_interface_api` SET `user_qps_limit` = `qps_limit`', 'SELECT 1 WHERE 0');
PREPARE bugpk_qps_stmt FROM @bugpk_qps_sql;
EXECUTE bugpk_qps_stmt;
DEALLOCATE PREPARE bugpk_qps_stmt;

-- ---------------------------------------------------------------------------
-- Step 3: rename `qps_limit` → `shared_qps_limit` with an updated comment
-- (skip if the rename was already applied).
-- ---------------------------------------------------------------------------
SET @bugpk_qps_sql = IF(EXISTS(
  SELECT 1 FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_qps_schema
    AND TABLE_NAME = 'bp_interface_api'
    AND COLUMN_NAME = 'shared_qps_limit'
), 'SELECT 1 WHERE 0',
  'ALTER TABLE `bp_interface_api` CHANGE COLUMN `qps_limit` `shared_qps_limit` int NOT NULL DEFAULT 0 COMMENT ''所有用户共享的接口总容量 QPS；0 表示不限制''');
PREPARE bugpk_qps_stmt FROM @bugpk_qps_sql;
EXECUTE bugpk_qps_stmt;
DEALLOCATE PREPARE bugpk_qps_stmt;

-- ---------------------------------------------------------------------------
-- Step 4: reset shared_qps_limit to 0 — the legacy value now lives in
-- user_qps_limit, so the shared ceiling defaults to "unlimited".
-- ---------------------------------------------------------------------------
UPDATE `bp_interface_api` SET `shared_qps_limit` = 0;


-- ============================================================
-- Migration: 000095_operation_log.up.sql
-- ============================================================

-- 系统操作日志（审计日志）。
--
-- 一条记录 = 一次「谁、什么时候、从哪、对什么、做了什么、结果如何」。
-- 设计要点：
--   1. 写多读少、只追加：无外键、无触发器，避免业务写入被审计表拖慢。
--   2. 读取永远按时间倒序分页：主键自增 + created_at 索引即可覆盖。
--   3. 维度筛选（用户 / 模块 / 动作 / 结果）走覆盖索引，避免回表。
--   4. client_ip 保留明文（管理员排障必需），client_ip_hash 供隐私安全聚合。
--   5. detail 用 JSON 存按需字段，不含请求体全文，避免审计表膨胀。

CREATE TABLE IF NOT EXISTS `bp_operation_log` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `user_id` bigint NOT NULL DEFAULT 0 COMMENT '操作者，0 表示匿名（登录失败/公开接口）',
  `username` varchar(64) NOT NULL DEFAULT '' COMMENT '冗余快照，用户改名/删除后仍可追溯',
  `module` varchar(32) NOT NULL DEFAULT '' COMMENT '归属模块，如 auth/user/interface',
  `action` varchar(64) NOT NULL DEFAULT '' COMMENT '动作码，如 login/logout/create/delete',
  `summary` varchar(255) NOT NULL DEFAULT '' COMMENT '人类可读摘要，列表直接展示',
  `method` varchar(8) NOT NULL DEFAULT '' COMMENT 'HTTP 方法',
  `path` varchar(255) NOT NULL DEFAULT '' COMMENT '请求路径（已剔除查询串敏感值）',
  `target_type` varchar(32) NOT NULL DEFAULT '' COMMENT '操作对象类型，如 interface/package',
  `target_id` varchar(64) NOT NULL DEFAULT '' COMMENT '操作对象主键',
  `status` tinyint NOT NULL DEFAULT 1 COMMENT '1=成功，0=失败',
  `error_message` varchar(512) NOT NULL DEFAULT '' COMMENT '失败原因，成功时为空',
  `status_code` int NOT NULL DEFAULT 0 COMMENT 'HTTP 状态码',
  `client_ip` varchar(64) NOT NULL DEFAULT '' COMMENT '来源 IP，管理员排障必需',
  `client_ip_hash` binary(32) DEFAULT NULL COMMENT 'IP 的 SHA-256，用于隐私安全聚合',
  `country` varchar(64) NOT NULL DEFAULT '',
  `province` varchar(64) NOT NULL DEFAULT '',
  `city` varchar(64) NOT NULL DEFAULT '',
  `user_agent` varchar(512) NOT NULL DEFAULT '',
  `elapsed_ms` int unsigned NOT NULL DEFAULT 0 COMMENT '服务端处理耗时',
  `detail` json DEFAULT NULL COMMENT '结构化补充字段，不含请求体全文',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  KEY `idx_bp_operation_log_created` (`created_at`),
  KEY `idx_bp_operation_log_user_time` (`user_id`, `created_at`),
  KEY `idx_bp_operation_log_module_time` (`module`, `created_at`),
  KEY `idx_bp_operation_log_action_time` (`action`, `created_at`),
  KEY `idx_bp_operation_log_status_time` (`status`, `created_at`),
  KEY `idx_bp_operation_log_ip_time` (`client_ip_hash`, `created_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='系统操作审计日志：登录、登出、增删改及敏感操作';

-- 操作日志按天滚动聚合表。
-- 操作日志是审计明细，全量在「统计卡片 / 趋势图」上聚合代价高。
-- 这里按天预聚合，让运营概览、告警、仪表盘的读取永远是常数级扫描。
-- 由 analytics worker 增量维护，与 bp_call_metric_minute 同一套机制。
CREATE TABLE IF NOT EXISTS `bp_operation_metric_day` (
  `bucket_date` date NOT NULL,
  `module` varchar(32) NOT NULL DEFAULT '',
  `action` varchar(64) NOT NULL DEFAULT '',
  `total_count` bigint unsigned NOT NULL DEFAULT 0,
  `success_count` bigint unsigned NOT NULL DEFAULT 0,
  `failure_count` bigint unsigned NOT NULL DEFAULT 0,
  `first_seen` datetime NOT NULL,
  `last_seen` datetime NOT NULL,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`bucket_date`, `module`, `action`),
  KEY `idx_bp_operation_metric_day_date` (`bucket_date`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='操作日志按天/模块/动作预聚合，供审计统计与告警使用';

-- 操作日志的 analytics 检查点，与 call_log_rollup 并列。
INSERT IGNORE INTO `bp_analytics_checkpoint` (`worker_key`) VALUES ('operation_log_rollup');

-- 菜单：系统操作日志。挂在「系统管理」(pid=30) 下，权限码 system:operation-log:list。
INSERT INTO `bp_menu`
  (`id`, `pid`, `name`, `path`, `component`, `redirect`, `type`, `auth_code`, `meta`, `status`)
VALUES
  (211, 30, 'SystemOperationLog', '/system/operation-log', '/system/operation-log/index', NULL, 'menu', 'system:operation-log:list',
   JSON_OBJECT('title', 'system.operationLog.title', 'icon', 'mdi:clipboard-text-clock-outline', 'order', 10), 1)
ON DUPLICATE KEY UPDATE
  `pid` = VALUES(`pid`),
  `path` = VALUES(`path`),
  `component` = VALUES(`component`),
  `redirect` = VALUES(`redirect`),
  `type` = VALUES(`type`),
  `auth_code` = VALUES(`auth_code`),
  `meta` = VALUES(`meta`),
  `status` = VALUES(`status`);

-- 授权给管理员角色（role_id=1），与其它菜单种子保持一致。
INSERT IGNORE INTO `bp_role_menu` (`role_id`, `menu_id`)
SELECT 1, id FROM `bp_menu` WHERE id = 211;


-- ============================================================
-- Migration: 000096_interface_billing_switches.up.sql
-- ============================================================

-- Interface-local billing switches. Existing behavior is preserved by DEFAULT 1.
ALTER TABLE bp_interface_api
  ADD COLUMN billing_enabled tinyint NOT NULL DEFAULT 1 COMMENT '接口计费总开关；0 不扣费且保留价格和规则',
  ADD COLUMN billing_log_enabled tinyint NOT NULL DEFAULT 1 COMMENT '是否展示新增计费日志并保留详细计费快照';

-- Keep the financial ledger for settlement/refund/reconciliation even when
-- optional billing logs are disabled. Capture visibility at invocation time.
ALTER TABLE bp_billing_record
  ADD COLUMN log_visible tinyint NOT NULL DEFAULT 1 COMMENT '计费日志可见性；不影响内部结算流水';


-- ============================================================
-- Migration: 000097_operation_log_actor_scope.up.sql
-- ============================================================

-- Event-time audience snapshot. Existing rows remain untouched/unclassified:
-- today's roles cannot reliably reconstruct historic roles or legacy noise.
-- Apply before deploying the scoped audit reader/writer. No historical backfill.
ALTER TABLE bp_operation_log
 ADD COLUMN actor_kind varchar(16) NOT NULL DEFAULT '' COMMENT 'Event-time admin/user; empty means legacy/unclassified',
 ADD KEY idx_bp_operation_log_actor_time (actor_kind, created_at),
 ADD KEY idx_bp_operation_log_user_actor_time (user_id, actor_kind, created_at);


-- ============================================================
-- Migration: 000098_idempotent_revenue_projection.up.sql
-- ============================================================

-- Income projection is separate from the legacy additive snapshot so older
-- binaries cannot contaminate the new total during an upgrade. Existing and
-- future confirmed rows become durable work via a default-zero marker.
ALTER TABLE bp_billing_record
  ADD COLUMN revenue_projected tinyint unsigned NOT NULL DEFAULT 0,
  ADD KEY idx_bp_billing_revenue_pending (status, revenue_projected, id, charge_amount);

CREATE TABLE IF NOT EXISTS bp_billing_revenue_projection (
  id tinyint unsigned NOT NULL,
  confirmed_charge_amount decimal(38,4) NOT NULL DEFAULT 0.0000,
  updated_at datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

INSERT INTO bp_billing_revenue_projection(id,confirmed_charge_amount)
VALUES(1,0.0000)
ON DUPLICATE KEY UPDATE id=VALUES(id);


-- ============================================================
-- Migration: 000099_rollup_hour_layer.up.sql
-- ============================================================

-- Hour-grain rollups decouple the 7/30-day dashboard windows from the
-- per-minute scan cost. The minute projections stay authoritative for the
-- near-real-time (<= 36h) curve; any wider window is served from these three
-- tables, cutting the scanned rows by up to 60x without changing the numbers.
--
-- The backfill is sourced from the already-aggregated minute projections, not
-- from the append-only call log, so an upgrade never re-scans raw traffic.
-- Every ON DUPLICATE KEY UPDATE overwrites (col=VALUES(col)) instead of adding
-- (col=col+VALUES(col)): the source window is recomputed exactly, so a re-run
-- replaces the same buckets rather than double counting them.

CREATE TABLE IF NOT EXISTS `bp_call_metric_hour` (
  `bucket_start` datetime NOT NULL,
  `call_count` bigint unsigned NOT NULL DEFAULT 0,
  `success_count` bigint unsigned NOT NULL DEFAULT 0,
  `failure_count` bigint unsigned NOT NULL DEFAULT 0,
  `billable_count` bigint unsigned NOT NULL DEFAULT 0,
  `charge_amount` decimal(38,4) NOT NULL DEFAULT 0.0000,
  `point_amount` decimal(38,0) NOT NULL DEFAULT 0,
  `request_bytes` decimal(38,0) NOT NULL DEFAULT 0,
  `response_bytes` decimal(38,0) NOT NULL DEFAULT 0,
  `elapsed_sum` decimal(38,0) NOT NULL DEFAULT 0,
  `elapsed_min` bigint unsigned NOT NULL DEFAULT 0,
  `elapsed_max` bigint unsigned NOT NULL DEFAULT 0,
  `first_seen` datetime NOT NULL,
  `last_seen` datetime NOT NULL,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`bucket_start`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='All-user call metrics by hour; the 7/30-day dashboard read layer';

CREATE TABLE IF NOT EXISTS `bp_call_user_metric_hour` (
  `bucket_start` datetime NOT NULL,
  `user_id` bigint NOT NULL,
  `call_count` bigint unsigned NOT NULL DEFAULT 0,
  `success_count` bigint unsigned NOT NULL DEFAULT 0,
  `failure_count` bigint unsigned NOT NULL DEFAULT 0,
  `billable_count` bigint unsigned NOT NULL DEFAULT 0,
  `charge_amount` decimal(38,4) NOT NULL DEFAULT 0.0000,
  `point_amount` decimal(38,0) NOT NULL DEFAULT 0,
  `request_bytes` decimal(38,0) NOT NULL DEFAULT 0,
  `response_bytes` decimal(38,0) NOT NULL DEFAULT 0,
  `elapsed_sum` decimal(38,0) NOT NULL DEFAULT 0,
  `elapsed_min` bigint unsigned NOT NULL DEFAULT 0,
  `elapsed_max` bigint unsigned NOT NULL DEFAULT 0,
  `first_seen` datetime NOT NULL,
  `last_seen` datetime NOT NULL,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`bucket_start`, `user_id`),
  KEY `idx_bp_call_user_metric_hour_user_time` (`user_id`, `bucket_start`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='Authoritative per-user call metrics by hour for current-account-aware totals';

CREATE TABLE IF NOT EXISTS `bp_call_api_metric_hour` (
  `bucket_start` datetime NOT NULL,
  `interface_id` bigint NOT NULL,
  `user_id` bigint NOT NULL,
  `api_code` varchar(64) NOT NULL DEFAULT '',
  `call_count` bigint unsigned NOT NULL DEFAULT 0,
  `success_count` bigint unsigned NOT NULL DEFAULT 0,
  `failure_count` bigint unsigned NOT NULL DEFAULT 0,
  `billable_count` bigint unsigned NOT NULL DEFAULT 0,
  `charge_amount` decimal(38,4) NOT NULL DEFAULT 0.0000,
  `point_amount` decimal(38,0) NOT NULL DEFAULT 0,
  `request_bytes` decimal(38,0) NOT NULL DEFAULT 0,
  `response_bytes` decimal(38,0) NOT NULL DEFAULT 0,
  `elapsed_sum` decimal(38,0) NOT NULL DEFAULT 0,
  `elapsed_min` bigint unsigned NOT NULL DEFAULT 0,
  `elapsed_max` bigint unsigned NOT NULL DEFAULT 0,
  `first_seen` datetime NOT NULL,
  `last_seen` datetime NOT NULL,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`bucket_start`, `interface_id`, `user_id`),
  KEY `idx_bp_call_api_metric_hour_user_time` (`user_id`, `bucket_start`),
  KEY `idx_bp_call_api_metric_hour_interface_time` (`interface_id`, `bucket_start`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='Per API and user call metrics by hour; replaces the minute grain for wide windows';

-- Backfill: collapse the minute projections into hour buckets. The window is
-- bounded to the hour layer's retention so the migration stays cheap on a
-- large minute table while still covering every bucket a reader can ask for.
INSERT INTO `bp_call_metric_hour` (
  `bucket_start`,`call_count`,`success_count`,`failure_count`,`billable_count`,
  `charge_amount`,`point_amount`,`request_bytes`,`response_bytes`,`elapsed_sum`,
  `elapsed_min`,`elapsed_max`,`first_seen`,`last_seen`
)
SELECT
  FROM_UNIXTIME(FLOOR(UNIX_TIMESTAMP(`bucket_start`)/3600)*3600),
  SUM(`call_count`),SUM(`success_count`),SUM(`failure_count`),SUM(`billable_count`),
  SUM(`charge_amount`),SUM(`point_amount`),SUM(`request_bytes`),SUM(`response_bytes`),
  SUM(`elapsed_sum`),MIN(`elapsed_min`),MAX(`elapsed_max`),MIN(`first_seen`),MAX(`last_seen`)
FROM `bp_call_metric_minute` FORCE INDEX (`PRIMARY`)
WHERE `bucket_start` >= DATE_SUB(NOW(), INTERVAL 180 DAY)
GROUP BY FROM_UNIXTIME(FLOOR(UNIX_TIMESTAMP(`bucket_start`)/3600)*3600)
ON DUPLICATE KEY UPDATE
  `call_count`=VALUES(`call_count`),
  `success_count`=VALUES(`success_count`),
  `failure_count`=VALUES(`failure_count`),
  `billable_count`=VALUES(`billable_count`),
  `charge_amount`=VALUES(`charge_amount`),
  `point_amount`=VALUES(`point_amount`),
  `request_bytes`=VALUES(`request_bytes`),
  `response_bytes`=VALUES(`response_bytes`),
  `elapsed_sum`=VALUES(`elapsed_sum`),
  `elapsed_min`=VALUES(`elapsed_min`),
  `elapsed_max`=VALUES(`elapsed_max`),
  `first_seen`=VALUES(`first_seen`),
  `last_seen`=VALUES(`last_seen`);

INSERT INTO `bp_call_user_metric_hour` (
  `bucket_start`,`user_id`,`call_count`,`success_count`,`failure_count`,`billable_count`,
  `charge_amount`,`point_amount`,`request_bytes`,`response_bytes`,`elapsed_sum`,
  `elapsed_min`,`elapsed_max`,`first_seen`,`last_seen`
)
SELECT
  FROM_UNIXTIME(FLOOR(UNIX_TIMESTAMP(`bucket_start`)/3600)*3600),`user_id`,
  SUM(`call_count`),SUM(`success_count`),SUM(`failure_count`),SUM(`billable_count`),
  SUM(`charge_amount`),SUM(`point_amount`),SUM(`request_bytes`),SUM(`response_bytes`),
  SUM(`elapsed_sum`),MIN(`elapsed_min`),MAX(`elapsed_max`),MIN(`first_seen`),MAX(`last_seen`)
FROM `bp_call_user_metric_minute` FORCE INDEX (`PRIMARY`)
WHERE `bucket_start` >= DATE_SUB(NOW(), INTERVAL 180 DAY)
GROUP BY FROM_UNIXTIME(FLOOR(UNIX_TIMESTAMP(`bucket_start`)/3600)*3600),`user_id`
ON DUPLICATE KEY UPDATE
  `call_count`=VALUES(`call_count`),
  `success_count`=VALUES(`success_count`),
  `failure_count`=VALUES(`failure_count`),
  `billable_count`=VALUES(`billable_count`),
  `charge_amount`=VALUES(`charge_amount`),
  `point_amount`=VALUES(`point_amount`),
  `request_bytes`=VALUES(`request_bytes`),
  `response_bytes`=VALUES(`response_bytes`),
  `elapsed_sum`=VALUES(`elapsed_sum`),
  `elapsed_min`=VALUES(`elapsed_min`),
  `elapsed_max`=VALUES(`elapsed_max`),
  `first_seen`=VALUES(`first_seen`),
  `last_seen`=VALUES(`last_seen`);

INSERT INTO `bp_call_api_metric_hour` (
  `bucket_start`,`interface_id`,`user_id`,`api_code`,`call_count`,`success_count`,
  `failure_count`,`billable_count`,`charge_amount`,`point_amount`,`request_bytes`,
  `response_bytes`,`elapsed_sum`,`elapsed_min`,`elapsed_max`,`first_seen`,`last_seen`
)
SELECT
  FROM_UNIXTIME(FLOOR(UNIX_TIMESTAMP(`bucket_start`)/3600)*3600),`interface_id`,`user_id`,
  MAX(`api_code`),SUM(`call_count`),SUM(`success_count`),SUM(`failure_count`),
  SUM(`billable_count`),SUM(`charge_amount`),SUM(`point_amount`),SUM(`request_bytes`),
  SUM(`response_bytes`),SUM(`elapsed_sum`),MIN(`elapsed_min`),MAX(`elapsed_max`),
  MIN(`first_seen`),MAX(`last_seen`)
FROM `bp_call_api_metric_minute` FORCE INDEX (`PRIMARY`)
WHERE `bucket_start` >= DATE_SUB(NOW(), INTERVAL 180 DAY)
GROUP BY FROM_UNIXTIME(FLOOR(UNIX_TIMESTAMP(`bucket_start`)/3600)*3600),`interface_id`,`user_id`
ON DUPLICATE KEY UPDATE
  `api_code`=VALUES(`api_code`),
  `call_count`=VALUES(`call_count`),
  `success_count`=VALUES(`success_count`),
  `failure_count`=VALUES(`failure_count`),
  `billable_count`=VALUES(`billable_count`),
  `charge_amount`=VALUES(`charge_amount`),
  `point_amount`=VALUES(`point_amount`),
  `request_bytes`=VALUES(`request_bytes`),
  `response_bytes`=VALUES(`response_bytes`),
  `elapsed_sum`=VALUES(`elapsed_sum`),
  `elapsed_min`=VALUES(`elapsed_min`),
  `elapsed_max`=VALUES(`elapsed_max`),
  `first_seen`=VALUES(`first_seen`),
  `last_seen`=VALUES(`last_seen`);


-- ============================================================
-- Migration: 000100_rollup_day_layer.up.sql
-- ============================================================

-- Day-grain rollups are the read layer for the 7-day trend, the active-user
-- ranking and the console trend/hot-API widgets. Those reads previously walked
-- the minute projections, so a single user-day cost 1,440 rows and a 7-day
-- window scanned days x users x 1,440 even though only one number per day is
-- ever displayed.
--
-- bp_call_metric_day has no user dimension at all, so it stays at one row per
-- day forever; bp_call_user_metric_day keeps the user dimension because the
-- account-status filter (bp_user join) is applied at read time and must remain
-- exact rather than being frozen into the aggregate.
--
-- Both tables are fed from the append-only call log by the rollup worker using
-- the same eligibility predicate as bp_call_metric_minute, so their numbers are
-- the same arithmetic over a coarser bucket. The worker aggregates one batch at
-- a time, therefore ON DUPLICATE KEY UPDATE must accumulate
-- (col=col+VALUES(col)); a replayed batch is only ever replayed when every
-- projection is empty (see rollupState.needsReplay), so accumulation cannot
-- double count.

CREATE TABLE IF NOT EXISTS `bp_call_metric_day` (
  `bucket_date` date NOT NULL,
  `call_count` bigint unsigned NOT NULL DEFAULT 0,
  `success_count` bigint unsigned NOT NULL DEFAULT 0,
  `failure_count` bigint unsigned NOT NULL DEFAULT 0,
  `billable_count` bigint unsigned NOT NULL DEFAULT 0,
  `charge_amount` decimal(38,4) NOT NULL DEFAULT 0.0000,
  `point_amount` decimal(38,0) NOT NULL DEFAULT 0,
  `request_bytes` decimal(38,0) NOT NULL DEFAULT 0,
  `response_bytes` decimal(38,0) NOT NULL DEFAULT 0,
  `elapsed_sum` decimal(38,0) NOT NULL DEFAULT 0,
  `elapsed_min` bigint unsigned NOT NULL DEFAULT 0,
  `elapsed_max` bigint unsigned NOT NULL DEFAULT 0,
  `first_seen` datetime NOT NULL,
  `last_seen` datetime NOT NULL,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`bucket_date`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='All-user call metrics by day; 1 row/day for dashboard trends';

CREATE TABLE IF NOT EXISTS `bp_call_user_metric_day` (
  `bucket_date` date NOT NULL,
  `user_id` bigint NOT NULL,
  `call_count` bigint unsigned NOT NULL DEFAULT 0,
  `success_count` bigint unsigned NOT NULL DEFAULT 0,
  `failure_count` bigint unsigned NOT NULL DEFAULT 0,
  `billable_count` bigint unsigned NOT NULL DEFAULT 0,
  `charge_amount` decimal(38,4) NOT NULL DEFAULT 0.0000,
  `point_amount` decimal(38,0) NOT NULL DEFAULT 0,
  `request_bytes` decimal(38,0) NOT NULL DEFAULT 0,
  `response_bytes` decimal(38,0) NOT NULL DEFAULT 0,
  `elapsed_sum` decimal(38,0) NOT NULL DEFAULT 0,
  `elapsed_min` bigint unsigned NOT NULL DEFAULT 0,
  `elapsed_max` bigint unsigned NOT NULL DEFAULT 0,
  `first_seen` datetime NOT NULL,
  `last_seen` datetime NOT NULL,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`bucket_date`,`user_id`),
  KEY `idx_bp_call_user_metric_day_user_time` (`user_id`,`bucket_date`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='Per-user call metrics by day; 1 row/user/day for active-user rankings';

-- Backfill from the already-aggregated minute projection instead of the raw
-- call log so an upgrade never re-scans append-only traffic. The window is
-- bounded to 180 days for the same reason the hour layer uses 180 days: wider
-- history is not read by any dashboard widget, and an unbounded GROUP BY over
-- the full minute table would be the longest statement of the migration.
INSERT INTO `bp_call_metric_day` (
  `bucket_date`,`call_count`,`success_count`,`failure_count`,`billable_count`,
  `charge_amount`,`point_amount`,`request_bytes`,`response_bytes`,`elapsed_sum`,
  `elapsed_min`,`elapsed_max`,`first_seen`,`last_seen`
)
SELECT
  DATE(`bucket_start`),SUM(`call_count`),SUM(`success_count`),SUM(`failure_count`),
  SUM(`billable_count`),SUM(`charge_amount`),SUM(`point_amount`),SUM(`request_bytes`),
  SUM(`response_bytes`),SUM(`elapsed_sum`),MIN(`elapsed_min`),MAX(`elapsed_max`),
  MIN(`first_seen`),MAX(`last_seen`)
FROM `bp_call_metric_minute` FORCE INDEX (`PRIMARY`)
WHERE `bucket_start` >= DATE_SUB(NOW(), INTERVAL 180 DAY)
GROUP BY DATE(`bucket_start`)
ON DUPLICATE KEY UPDATE
  `call_count`=VALUES(`call_count`),
  `success_count`=VALUES(`success_count`),
  `failure_count`=VALUES(`failure_count`),
  `billable_count`=VALUES(`billable_count`),
  `charge_amount`=VALUES(`charge_amount`),
  `point_amount`=VALUES(`point_amount`),
  `request_bytes`=VALUES(`request_bytes`),
  `response_bytes`=VALUES(`response_bytes`),
  `elapsed_sum`=VALUES(`elapsed_sum`),
  `elapsed_min`=VALUES(`elapsed_min`),
  `elapsed_max`=VALUES(`elapsed_max`),
  `first_seen`=VALUES(`first_seen`),
  `last_seen`=VALUES(`last_seen`);

INSERT INTO `bp_call_user_metric_day` (
  `bucket_date`,`user_id`,`call_count`,`success_count`,`failure_count`,`billable_count`,
  `charge_amount`,`point_amount`,`request_bytes`,`response_bytes`,`elapsed_sum`,
  `elapsed_min`,`elapsed_max`,`first_seen`,`last_seen`
)
SELECT
  DATE(`bucket_start`),`user_id`,SUM(`call_count`),SUM(`success_count`),SUM(`failure_count`),
  SUM(`billable_count`),SUM(`charge_amount`),SUM(`point_amount`),SUM(`request_bytes`),
  SUM(`response_bytes`),SUM(`elapsed_sum`),MIN(`elapsed_min`),MAX(`elapsed_max`),
  MIN(`first_seen`),MAX(`last_seen`)
FROM `bp_call_user_metric_minute` FORCE INDEX (`PRIMARY`)
WHERE `bucket_start` >= DATE_SUB(NOW(), INTERVAL 180 DAY)
GROUP BY DATE(`bucket_start`),`user_id`
ON DUPLICATE KEY UPDATE
  `call_count`=VALUES(`call_count`),
  `success_count`=VALUES(`success_count`),
  `failure_count`=VALUES(`failure_count`),
  `billable_count`=VALUES(`billable_count`),
  `charge_amount`=VALUES(`charge_amount`),
  `point_amount`=VALUES(`point_amount`),
  `request_bytes`=VALUES(`request_bytes`),
  `response_bytes`=VALUES(`response_bytes`),
  `elapsed_sum`=VALUES(`elapsed_sum`),
  `elapsed_min`=VALUES(`elapsed_min`),
  `elapsed_max`=VALUES(`elapsed_max`),
  `first_seen`=VALUES(`first_seen`),
  `last_seen`=VALUES(`last_seen`);


-- ============================================================
-- Migration: 000101_interface_doc_response_formats.up.sql
-- ============================================================

-- Multi-format public docs response. An optional JSON array of
-- {"type","example"} entries replaces the single doc_response_type/
-- doc_response_example pair when present; NULL keeps the legacy pair
-- authoritative so existing interfaces need no backfill.
ALTER TABLE `bp_interface_api`
  ADD COLUMN `doc_response_formats` json DEFAULT NULL
    COMMENT 'Public doc response formats [{type,example}]; NULL falls back to doc_response_type/doc_response_example'
    AFTER `doc_response_example`;


-- ============================================================
-- Migration: 000102_redeem_code.up.sql
-- ============================================================

-- Redeem codes (CDK) turn a typed reward template into single-use plaintext
-- codes. Three tables carry the whole feature: the template, the codes and the
-- per-account redemption ledger.
--
-- Concurrency safety is deliberately pushed into unique keys instead of
-- application locks:
--   * uk_redeem_code_plain            - generation de-duplication plus O(1)
--                                       lookup by code during redemption.
--   * uk_redeem_record_user_code      - the same account can never burn the
--                                       same code twice.
--   * uk_redeem_record_user_type_lock - when per_user_limit = 1 the record
--                                       stores type_lock = type_id, so a
--                                       second code of the same type from the
--                                       same account collides at the DB layer.
--                                       Unlimited / limit-N types store NULL
--                                       and MySQL ignores NULLs in unique
--                                       indexes, leaving the row-lock path
--                                       (bp_user FOR UPDATE + COUNT) in
--                                       charge.
--   * uk_redeem_record_type_ip        - when the effective per_ip_limit = 1
--                                       the record stores ip_lock = client_ip
--                                       and the key becomes (type_id, ip_lock):
--                                       one redemption per account-less IP per
--                                       type. The design note originally
--                                       spelled this key (ip_lock, type_lock),
--                                       which would silently lose the type
--                                       dimension for unlimited types
--                                       (type_lock = NULL) and over-block one
--                                       redemption per IP across every type;
--                                       keying on the real type_id keeps the
--                                       four limit combinations independent.
--
-- Rewards settle inside the redemption transaction with the same guarded
-- UPDATE pattern as payment applySettlement (points/balance capacity guards,
-- affected = 0 rolls back) and one bp_wallet_transaction row per award with
-- biz_type = 'REDEEM_CODE'. Package rewards copy the full benefit snapshot so
-- the "My packages" page shows exactly what an Alipay purchase would show.
--
-- Codes are stored in plaintext on purpose: the console must display and
-- export them (PRD hard requirement), the unique index doubles as the
-- generation de-duplicator, and guessing is defended by the 31^16 keyspace
-- plus the unified "invalid" message instead of hashing.

CREATE TABLE IF NOT EXISTS `bp_redeem_code_type` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '兑换码类型编号',
  `code` varchar(32) COLLATE utf8mb4_bin NOT NULL COMMENT '类型标识，唯一',
  `name` varchar(64) NOT NULL COMMENT '类型展示名',
  `reward_type` tinyint NOT NULL COMMENT '1=点数 2=余额 3=全站套餐 4=接口套餐规格 5=组合(点数+余额)',
  `reward_points` bigint NOT NULL DEFAULT 0 COMMENT '点数面值',
  `reward_balance` decimal(12,4) NOT NULL DEFAULT 0.0000 COMMENT '余额面值',
  `reward_ref_id` bigint NOT NULL DEFAULT 0 COMMENT '3=bp_package_global.id 4=bp_package_interface_spec.id，其余恒 0',
  `per_user_limit` int NOT NULL DEFAULT 0 COMMENT '0=不限次 1=每账号仅1次 N>=2=每账号限N次',
  `per_ip_limit` int NOT NULL DEFAULT 0 COMMENT '0=不限 N>=1=每IP限N次（活动发放增量）',
  `start_at` datetime NOT NULL COMMENT '类型有效期起',
  `end_at` datetime NOT NULL COMMENT '类型有效期止',
  `status` tinyint NOT NULL DEFAULT 1 COMMENT '1=启用 0=停用',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_redeem_code_type_code` (`code`),
  KEY `idx_redeem_code_type_status` (`status`, `created_at`),
  CONSTRAINT `chk_redeem_type_reward_matrix` CHECK (
    (`reward_type` = 1 AND `reward_points` > 0 AND `reward_balance` = 0 AND `reward_ref_id` = 0) OR
    (`reward_type` = 2 AND `reward_points` = 0 AND `reward_balance` > 0 AND `reward_ref_id` = 0) OR
    (`reward_type` IN (3, 4) AND `reward_points` = 0 AND `reward_balance` = 0 AND `reward_ref_id` > 0) OR
    (`reward_type` = 5 AND `reward_points` > 0 AND `reward_balance` > 0 AND `reward_ref_id` = 0)
  ),
  CONSTRAINT `chk_redeem_type_reward_type` CHECK (`reward_type` IN (1, 2, 3, 4, 5)),
  CONSTRAINT `chk_redeem_type_per_user_limit` CHECK (`per_user_limit` >= 0),
  CONSTRAINT `chk_redeem_type_per_ip_limit` CHECK (`per_ip_limit` >= 0),
  CONSTRAINT `chk_redeem_type_status` CHECK (`status` IN (0, 1)),
  CONSTRAINT `chk_redeem_type_window` CHECK (`start_at` < `end_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='兑换码类型模板';

-- Frozen after generation: reward_type / reward_points / reward_balance /
-- reward_ref_id / per_user_limit are rejected by the service once the type has
-- produced codes (changing a face value would reach already-generated codes,
-- and relaxing a limit would enlarge benefits already granted). Only name /
-- window / status stay editable, and those edits are operation-logged.

CREATE TABLE IF NOT EXISTS `bp_redeem_code` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '兑换码编号',
  `type_id` bigint unsigned NOT NULL COMMENT '所属类型',
  `code_plain` varchar(19) COLLATE utf8mb4_bin NOT NULL COMMENT '明文兑换码 XXXX-XXXX-XXXX-XXXX',
  `status` tinyint NOT NULL DEFAULT 1 COMMENT '1=未用 2=已用 0=已作废',
  `batch_id` bigint DEFAULT NULL COMMENT '生成批次，见 bp_redeem_generation_batch',
  `used_by` bigint DEFAULT NULL COMMENT '核销用户',
  `used_at` datetime DEFAULT NULL COMMENT '核销时间',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_redeem_code_plain` (`code_plain`),
  KEY `idx_redeem_code_type_status` (`type_id`, `status`),
  KEY `idx_redeem_code_batch` (`batch_id`),
  CONSTRAINT `chk_redeem_code_status` CHECK (`status` IN (0, 1, 2))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='兑换码';

CREATE TABLE IF NOT EXISTS `bp_redeem_record` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '兑换记录编号',
  `user_id` bigint NOT NULL COMMENT '兑换用户',
  `code_id` bigint unsigned NOT NULL COMMENT '兑换码',
  `type_id` bigint unsigned NOT NULL COMMENT '类型',
  `type_lock` bigint unsigned DEFAULT NULL COMMENT 'per_user_limit=1 时=type_id，否则 NULL',
  `ip_lock` varchar(45) COLLATE utf8mb4_bin DEFAULT NULL COMMENT '生效 per_ip_limit=1 时=归一化 client_ip，否则 NULL',
  `reward_type` tinyint NOT NULL COMMENT '奖励类型快照',
  `reward_points` bigint NOT NULL DEFAULT 0 COMMENT '发放点数快照',
  `reward_balance` decimal(12,4) NOT NULL DEFAULT 0.0000 COMMENT '发放余额快照',
  `reward_name` varchar(128) DEFAULT NULL COMMENT '奖励名称快照（套餐名/套餐名-规格名），防事后改名漂移',
  `valid_days` int DEFAULT NULL COMMENT '套餐有效期天数快照',
  `client_ip` varchar(45) COLLATE utf8mb4_bin DEFAULT NULL COMMENT '归一化来源 IP；IPv4-mapped IPv6 已归一；空=未取到',
  `batch_id` bigint DEFAULT NULL COMMENT '生成批次',
  `source` tinyint NOT NULL DEFAULT 1 COMMENT '1=页面兑换 2=开放 API',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_redeem_record_user_code` (`user_id`, `code_id`),
  UNIQUE KEY `uk_redeem_record_user_type_lock` (`user_id`, `type_lock`),
  UNIQUE KEY `uk_redeem_record_type_ip` (`type_id`, `ip_lock`),
  KEY `idx_redeem_record_user_time` (`user_id`, `created_at`),
  KEY `idx_redeem_record_type_time` (`type_id`, `created_at`),
  CONSTRAINT `chk_redeem_record_source` CHECK (`source` IN (1, 2))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='兑换记录';

-- Generation batches snapshot the effective limits at generation time so an
-- in-flight campaign keeps its rules after an admin edits the template, and so
-- API-generated codes carry a single traceable origin row instead of repeating
-- campaign/source columns on every code.
CREATE TABLE IF NOT EXISTS `bp_redeem_generation_batch` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '批次编号',
  `type_id` bigint unsigned NOT NULL COMMENT '类型',
  `count` int NOT NULL COMMENT '本批生成数量',
  `per_user_limit_snap` int NOT NULL COMMENT '生成时生效的每账号限领',
  `per_ip_limit_snap` int NOT NULL COMMENT '生成时生效的每 IP 限领',
  `campaign_ref` varchar(64) COLLATE utf8mb4_bin DEFAULT NULL COMMENT '活动引用标识（插件活动名/ID）',
  `source` tinyint NOT NULL DEFAULT 1 COMMENT '1=管理端生成 2=开放 API 生成',
  `creator_key_id` bigint DEFAULT NULL COMMENT '开放 API Key 编号（source=2 时追溯）',
  `created_by` bigint DEFAULT NULL COMMENT '操作人',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  KEY `idx_redeem_batch_type_time` (`type_id`, `created_at`),
  CONSTRAINT `chk_redeem_batch_source` CHECK (`source` IN (1, 2))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='兑换码生成批次';

-- Per-IP counters only exist for per_ip_limit >= 2 (limit = 1 is enforced by
-- the uk_redeem_record_type_ip unique key alone). The INSERT ... ON DUPLICATE
-- KEY UPDATE used=used+1 row lock serializes concurrent redemptions from the
-- same IP until commit.
CREATE TABLE IF NOT EXISTS `bp_redeem_ip_counter` (
  `type_id` bigint unsigned NOT NULL COMMENT '类型',
  `client_ip` varchar(45) COLLATE utf8mb4_bin NOT NULL COMMENT '归一化 IP',
  `used` bigint NOT NULL DEFAULT 0 COMMENT '已用次数',
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`type_id`, `client_ip`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='兑换码每 IP 限领计数器';

-- Guard rails mirror the auth verification-code pattern: limits live in a DB
-- singleton (not config.yaml), every attempt counts (success and failure), and
-- a Redis outage fails closed so a cache incident can never accelerate code
-- probing. Defaults are deliberately generous: the 31^16 keyspace already
-- makes brute force infeasible, so these are backstops for scripted abuse,
-- not UX limits.
CREATE TABLE IF NOT EXISTS `bp_redeem_guard_config` (
  `id` tinyint NOT NULL DEFAULT 1 COMMENT '单例 id',
  `ip_minute_limit` int NOT NULL DEFAULT 60 COMMENT '单 IP 每分钟尝试上限，<=0 不限',
  `ip_hour_limit` int NOT NULL DEFAULT 600 COMMENT '单 IP 每小时尝试上限，<=0 不限',
  `ip_day_limit` int NOT NULL DEFAULT 3000 COMMENT '单 IP 每天尝试上限，<=0 不限',
  `user_minute_limit` int NOT NULL DEFAULT 20 COMMENT '单账号每分钟尝试上限，<=0 不限',
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  CONSTRAINT `chk_redeem_guard_singleton` CHECK (`id` = 1)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='兑换防刷频控配置';

INSERT INTO `bp_redeem_guard_config` (`id`, `ip_minute_limit`, `ip_hour_limit`, `ip_day_limit`, `user_minute_limit`)
VALUES (1, 60, 600, 3000, 20)
ON DUPLICATE KEY UPDATE `id` = VALUES(`id`);

-- Console menus. Buttons carry the auth codes (the frontend never hardcodes
-- permissions; /auth/codes is the source of truth), the page menus carry the
-- list codes. Grant to the admin role only; the user-facing entry lives inside
-- the existing asset center page and needs no menu.
INSERT INTO `bp_menu`
  (`id`, `pid`, `name`, `path`, `component`, `redirect`, `type`, `auth_code`, `meta`, `status`)
VALUES
  (220, 0, 'RedeemCode', '/redeem', NULL, '/redeem/types', 'catalog', NULL, JSON_OBJECT('title', 'system.redeem.title', 'icon', 'lucide:ticket-percent', 'order', 9986), 1),
  (221, 220, 'RedeemTypes', '/redeem/types', '/redeem/types/index', NULL, 'menu', 'redeem:type:list', JSON_OBJECT('title', 'system.redeem.type.title', 'order', 1), 1),
  (222, 220, 'RedeemCodes', '/redeem/codes', '/redeem/codes/index', NULL, 'menu', 'redeem:code:list', JSON_OBJECT('title', 'system.redeem.code.title', 'order', 2), 1),
  (223, 220, 'RedeemRecords', '/redeem/records', '/redeem/records/index', NULL, 'menu', 'redeem:record:list', JSON_OBJECT('title', 'system.redeem.record.title', 'order', 3), 1),
  (224, 221, 'RedeemTypeCreate', '', NULL, NULL, 'button', 'redeem:type:create', JSON_OBJECT('title', 'system.redeem.type.create', 'order', 1), 1),
  (225, 221, 'RedeemTypeUpdate', '', NULL, NULL, 'button', 'redeem:type:update', JSON_OBJECT('title', 'system.redeem.type.update', 'order', 2), 1),
  (226, 222, 'RedeemCodeGenerate', '', NULL, NULL, 'button', 'redeem:code:generate', JSON_OBJECT('title', 'system.redeem.code.generate', 'order', 1), 1),
  (227, 222, 'RedeemCodeVoid', '', NULL, NULL, 'button', 'redeem:code:void', JSON_OBJECT('title', 'system.redeem.code.void', 'order', 2), 1),
  (228, 222, 'RedeemCodeExport', '', NULL, NULL, 'button', 'redeem:code:export', JSON_OBJECT('title', 'system.redeem.code.export', 'order', 3), 1)
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
SELECT 1, `id` FROM `bp_menu` WHERE `id` BETWEEN 220 AND 228;


-- ============================================================
-- Migration: 000103_open_api.up.sql
-- ============================================================

-- System Open API introduces a second, independent credential family. It
-- shares nothing with the data-plane API keys (bp_api_key): separate table,
-- separate hash, separate auth header (X-Open-Key), separate route prefix
-- (/open-api). Mixing the two would let a leaked traffic key escalate into
-- account management, so no foreign keys or joins exist between them.
--
-- Three switches decide whether a key works at all, evaluated in order:
--   1. the global singleton below (default OFF - the feature ships dark),
--   2. bp_user.open_api_admin_disabled (admin force-disable, highest priority),
--   3. bp_user.open_api_enabled (user self-service switch).
-- The user switch is evaluated live: flipping it invalidates or restores the
-- account's keys without touching key rows, so re-enabling needs no re-issue.
--
-- At most one active key per account is enforced at the DB layer with a
-- STORED generated column (if(status=1, user_id, NULL)) plus a UNIQUE index;
-- NULLs are ignored by MySQL unique indexes, the same trick the redeem
-- type_lock column uses, so revocation frees the slot and concurrent
-- issuance cannot produce two live keys.
--
-- Scopes are a per-endpoint checklist keyed by stable endpoint codes. The
-- is_admin_scope flag is derived display state (set when the scope list
-- contains openapi:redeem-code:generate); the prefix opka_ is an identifier
-- only. Authorization is always the scope list intersected with the holder's
-- live RBAC, so a demoted admin loses admin-scope calls immediately.

CREATE TABLE IF NOT EXISTS `bp_open_api_config` (
  `id` tinyint NOT NULL DEFAULT 1 COMMENT '单例 id',
  `enabled` tinyint NOT NULL DEFAULT 0 COMMENT '全局总开关：1=开放 /open-api，0=全部 403 openapi.disabled',
  `ip_whitelist_max` int NOT NULL DEFAULT 10 COMMENT '单 Key IP 白名单条数上限',
  `key_default_expire_days` int NOT NULL DEFAULT 90 COMMENT '用户自助签发 Key 默认有效期（天）',
  `admin_key_expire_days` int NOT NULL DEFAULT 30 COMMENT '管理员作用域 Key 默认有效期（天），不可为空',
  `rate_limit_normal` int NOT NULL DEFAULT 60 COMMENT '普通 Key 每分钟调用上限，<=0 不限',
  `rate_limit_admin` int NOT NULL DEFAULT 30 COMMENT '管理员作用域 Key 每分钟调用上限，<=0 不限',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  CONSTRAINT `chk_open_api_config_singleton` CHECK (`id` = 1),
  CONSTRAINT `chk_open_api_config_enabled` CHECK (`enabled` IN (0, 1)),
  CONSTRAINT `chk_open_api_config_ip_max` CHECK (`ip_whitelist_max` >= 0),
  CONSTRAINT `chk_open_api_config_key_days` CHECK (`key_default_expire_days` > 0),
  CONSTRAINT `chk_open_api_config_admin_days` CHECK (`admin_key_expire_days` > 0),
  CONSTRAINT `chk_open_api_config_rate_normal` CHECK (`rate_limit_normal` >= 0),
  CONSTRAINT `chk_open_api_config_rate_admin` CHECK (`rate_limit_admin` >= 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='系统开放 API 全局配置';

INSERT INTO `bp_open_api_config` (`id`, `enabled`, `ip_whitelist_max`, `key_default_expire_days`, `admin_key_expire_days`, `rate_limit_normal`, `rate_limit_admin`)
VALUES (1, 0, 10, 90, 30, 60, 30)
ON DUPLICATE KEY UPDATE `id` = VALUES(`id`);

-- Per-user switches live on bp_user itself: they share the account's
-- lifecycle and the auth chain needs them in the same single JOIN that
-- resolves the key. Defaults are "allowed" because the global switch ships
-- off; once an admin opens the feature, self-service works without a second
-- opt-in step.
ALTER TABLE `bp_user`
  ADD COLUMN `open_api_enabled` tinyint NOT NULL DEFAULT 1 COMMENT '开放 API 用户自助开关：1=名下 Key 可生效',
  ADD COLUMN `open_api_admin_disabled` tinyint NOT NULL DEFAULT 0 COMMENT '管理员强制禁用开放 API（优先级最高）';

CREATE TABLE IF NOT EXISTS `bp_open_api_key` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '开放 Key 编号',
  `user_id` bigint NOT NULL COMMENT '归属账号',
  `name` varchar(64) NOT NULL COMMENT '备注名',
  `key_hash` char(64) COLLATE utf8mb4_bin NOT NULL COMMENT 'SHA-256(明文) 十六进制',
  `key_prefix` varchar(8) COLLATE utf8mb4_bin NOT NULL COMMENT '明文前 4 位，掩码展示',
  `key_suffix` varchar(8) COLLATE utf8mb4_bin NOT NULL COMMENT '明文后 4 位，掩码展示',
  `is_admin_scope` tinyint NOT NULL DEFAULT 0 COMMENT '派生展示字段：scope 含 openapi:redeem-code:generate 时置 1',
  `status` tinyint NOT NULL DEFAULT 1 COMMENT '1=启用 0=吊销（不可改回；重置=旧行吊销+新行）',
  `active_user_id` bigint GENERATED ALWAYS AS (if(`status` = 1, `user_id`, NULL)) STORED COMMENT '生成列：status=1 时=user_id，UNIQUE 实现每用户仅 1 个有效 Key',
  `expires_at` datetime NOT NULL COMMENT '过期时间（管理员作用域不可为空）',
  `last_used_at` datetime DEFAULT NULL COMMENT '最近一次成功调用',
  `revoked_at` datetime DEFAULT NULL COMMENT '吊销时间（status 1→0 时写入，供审计追溯）',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_open_api_key_hash` (`key_hash`),
  UNIQUE KEY `uk_open_api_key_active_user` (`active_user_id`),
  KEY `idx_open_api_key_user_status` (`user_id`, `status`),
  CONSTRAINT `chk_open_api_key_status` CHECK (`status` IN (0, 1)),
  CONSTRAINT `chk_open_api_key_admin_scope` CHECK (`is_admin_scope` IN (0, 1))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='系统开放 Key';

-- One row per authorized endpoint. The middleware rejects a call whose
-- endpoint code is missing from this list with 403 openapi.scope_denied
-- before any business logic runs.
CREATE TABLE IF NOT EXISTS `bp_open_api_key_scope` (
  `key_id` bigint unsigned NOT NULL COMMENT '开放 Key',
  `endpoint_code` varchar(48) COLLATE utf8mb4_bin NOT NULL COMMENT '稳定端点 code，如 openapi:account:read',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (`key_id`, `endpoint_code`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='开放 Key 接口级 scope 清单';

-- Independent whitelist table: the data-plane bp_api_key_ip_whitelist is
-- bound to traffic keys by foreign key semantics and must not be reused.
-- An empty table means "no IP restriction" for ordinary keys; admin-scope
-- keys are required to have at least one rule at issuance time.
CREATE TABLE IF NOT EXISTS `bp_open_api_key_ip_whitelist` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '白名单条目编号',
  `key_id` bigint unsigned NOT NULL COMMENT '开放 Key',
  `ip_rule` varchar(64) COLLATE utf8mb4_bin NOT NULL COMMENT '单 IP 或 CIDR（0.0.0.0/0 与 ::/0 拒绝）',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_open_api_key_ip_rule` (`key_id`, `ip_rule`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='开放 Key IP 白名单';

-- Console menus. The admin config page and the cross-account key management
-- page hang off System Management; the user-facing page is a sibling of the
-- asset center under the same parent catalog.
INSERT INTO `bp_menu`
  (`id`, `pid`, `name`, `path`, `component`, `redirect`, `type`, `auth_code`, `meta`, `status`)
VALUES
  (230, 30, 'SystemOpenApiConfig', '/system/open-api-config', '/system/open-api-config/index', NULL, 'menu', 'system:open-api:config', JSON_OBJECT('title', 'system.openApi.configTitle', 'icon', 'lucide:shield-check', 'order', 7), 1),
  (231, 230, 'SystemOpenApiConfigUpdate', '', NULL, NULL, 'button', 'system:open-api:config:update', JSON_OBJECT('title', 'system.openApi.configUpdate', 'order', 1), 1),
  (232, 30, 'SystemOpenApiKeys', '/system/open-api-keys', '/system/open-api-keys/index', NULL, 'menu', 'openapi:key:list', JSON_OBJECT('title', 'system.openApi.keyTitle', 'icon', 'lucide:key-round', 'order', 8), 1),
  (233, 232, 'OpenApiKeyCreate', '', NULL, NULL, 'button', 'openapi:key:create', JSON_OBJECT('title', 'system.openApi.keyCreate', 'order', 1), 1),
  (234, 232, 'OpenApiKeyRevoke', '', NULL, NULL, 'button', 'openapi:key:revoke', JSON_OBJECT('title', 'system.openApi.keyRevoke', 'order', 2), 1),
  (235, 232, 'OpenApiUserDisable', '', NULL, NULL, 'button', 'openapi:user:disable', JSON_OBJECT('title', 'system.openApi.userDisable', 'order', 3), 1),
  (240, 84, 'UserOpenApiKey', '/purchase/open-api-key', '/purchase/open-api-key/index', NULL, 'menu', 'openapi:self:read', JSON_OBJECT('title', 'system.openApi.userTitle', 'icon', 'lucide:key-round', 'order', 6), 1)
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
SELECT 1, `id` FROM `bp_menu` WHERE `id` BETWEEN 230 AND 235;

INSERT IGNORE INTO `bp_role_menu` (`role_id`, `menu_id`)
SELECT `id`, 240 FROM `bp_role` WHERE `role_key` IN ('admin', 'user');


-- ============================================================
-- Migration: 000104_captcha_provider_inheritance.up.sql
-- ============================================================

-- Distinguish an administrator's explicit provider choice from the stock seed
-- rows. Every authentication scene ships with its own enabled `pow` row
-- (migration 000001), and the resolver prefers a scene's own row over the
-- `default` row, so switching the `default` scene to 极验 / Cloudflare could
-- never take effect for a real scene. `explicit_provider` makes the intended
-- precedence explicit: a scene's explicit row wins, then the default scene's
-- explicit row (inherited), then the scene's seed row, then the built-in POW
-- fallback.
ALTER TABLE `bp_captcha_provider_config`
  ADD COLUMN `explicit_provider` tinyint NOT NULL DEFAULT 0
    COMMENT 'Whether the administrator explicitly selected this provider: 1 explicit, 0 seed/inherited'
    AFTER `scene_enabled`;

-- Rows that are not the stock `pow` seed, or that already carry third-party
-- credentials, reflect an administrator decision and must stay explicit.
UPDATE `bp_captcha_provider_config`
SET `explicit_provider` = 1
WHERE `provider_key` <> 'pow'
   OR COALESCE(`site_key`, '') <> ''
   OR COALESCE(`secret_ref`, '') <> '';


-- ============================================================
-- Migration: 000105_register_risk_guard.up.sql
-- ============================================================

-- Register risk guard (anti bulk-registration).
--
-- 1) bp_auth_register_config.register_ip_max_accounts caps how many accounts
--    may share one registration IP (0 = disabled, preserves legacy behavior).
--    The per-IP *rate* limit (register_ip_hour_limit) throttles attempts; this
--    new column is a hard lifetime cap enforced by bp_register_ip_used.
-- 2) bp_email_domain_rule holds the admin-managed email domain policy:
--    `whitelist` rows allow-list the only domains that may register (a
--    non-empty whitelist makes registration exclusive to those domains) and
--    `blacklist` rows reject disposable/abused domains outright.
-- 3) bp_register_ip_used records one row per account created from an IP. The
--    primary key on ip_hash makes the 1-account-per-IP cap race-free at the
--    storage layer; ip_hash is iphash.RiskDigest(client_ip) so the table never
--    stores a plaintext IP.
ALTER TABLE `bp_auth_register_config`
  ADD COLUMN `register_ip_max_accounts` int NOT NULL DEFAULT 0
    COMMENT 'Lifetime cap of accounts per registration IP; 0 disables the cap'
    AFTER `register_ip_hour_limit`;

CREATE TABLE `bp_email_domain_rule` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `rule_kind` varchar(16) NOT NULL COMMENT 'whitelist or blacklist',
  `domain` varchar(255) NOT NULL COMMENT 'lower-case email domain, no @',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_email_domain_rule` (`rule_kind`, `domain`),
  CONSTRAINT `chk_email_domain_rule_kind` CHECK (`rule_kind` in ('whitelist', 'blacklist'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='Registration email domain whitelist/blacklist';

CREATE TABLE `bp_register_ip_used` (
  `ip_hash` binary(32) NOT NULL COMMENT 'iphash.RiskDigest(client_ip) at registration time',
  `user_id` bigint unsigned NOT NULL,
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (`ip_hash`),
  KEY `idx_register_ip_user` (`user_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='One row per account created from a registration IP (keyed hash, no plaintext IP)';


-- ============================================================
-- Migration: 000106_analytics_daily_hole_repair.up.sql
-- ============================================================

-- Request an idempotent, bounded repair when a retained minute day has no
-- matching durable daily projection. This fixes pre-000100 historical holes
-- without replaying or deleting history that is no longer in the source log.
--
-- Run with the updated binary: its day repair window is frozen at the source
-- floor and retention protects all PENDING/RUNNING repair rows.
-- Persisted bit order 0..10 is unchanged; mask=2047 repairs all current
-- projections together. A historical day hole may coexist with >1000 missing
-- minute contributions, so a day-only repair cannot certify the recovered window.
-- Do NOT reset an active task or checkpoint. Re-running the standalone upgrade
-- only requests work while an actual missing day still exists.
UPDATE bp_analytics_checkpoint c
SET c.repair_status='PENDING',
    c.repair_phase='IDLE',
    c.repair_projection_mask=2047,
    c.repair_window_days=31,
    c.repair_requested_at=NOW(6),
    c.repair_started_at=NULL,
    c.repair_completed_at=NULL,
    c.repair_window_start=NULL,
    c.repair_upper_log_id=0,
    c.repair_cursor_id=0,
    c.repair_clear_projection=0
WHERE c.worker_key='call_log_rollup'
  AND COALESCE(c.repair_status,'NONE') NOT IN ('PENDING','RUNNING')
  AND COALESCE(c.repair_phase,'IDLE') NOT IN ('CLEAR','ROLLUP','FINALIZE')
  AND EXISTS (
    SELECT 1 FROM (
      SELECT DATE(bucket_start) AS bucket_date,SUM(call_count) AS calls
      FROM bp_call_metric_minute
      WHERE bucket_start>=DATE_SUB(CURRENT_DATE(),INTERVAL 3 DAY)
        AND bucket_start<DATE_ADD(CURRENT_DATE(),INTERVAL 1 DAY)
      GROUP BY DATE(bucket_start)
    ) m
    WHERE m.calls>0 AND (
      NOT EXISTS(SELECT 1 FROM bp_call_metric_day d WHERE d.bucket_date=m.bucket_date AND d.call_count>0)
      OR NOT EXISTS(SELECT 1 FROM bp_call_api_metric_day d WHERE d.bucket_date=m.bucket_date AND d.call_count>0)
      OR NOT EXISTS(SELECT 1 FROM bp_call_user_metric_day d WHERE d.bucket_date=m.bucket_date AND d.call_count>0)
    )
  );


-- ============================================================
-- Migration: 000107_call_log_body_extract.up.sql
-- ============================================================

-- 000107: extract the call-log response body into a side table.
--
-- bp_interface_call_log.response_body is a MEDIUMTEXT that carries a full,
-- masked-and-truncated response JSON per call. Keeping it inline bloats every
-- clustered row and every secondary index rebuild, which is the dominant driver
-- of the table's several-hundred-GB footprint and ~40 GB/day write volume.
--
-- This migration creates the side table WITHOUT backfilling the existing log.
-- A backfill would have to scan the entire multi-hundred-GB source table during
-- deployment, holding locks for hours — exactly what the upgrade contract
-- forbids. Instead:
--   - New writes put the body only in the side table (the main column is left
--     NULL for new rows).
--   - Reads use coalesce(b.response_body, l.response_body): rows written before
--     this migration keep serving their body from the main column.
--   - The main column is dropped later in a separately-scheduled maintenance
--     window, after the read path no longer references it.
--
-- The application writes the body in the same transaction as the main insert,
-- so a main row can never exist without its body (and a body row is always
-- attached to a committed main row via the shared transaction).

CREATE TABLE IF NOT EXISTS `bp_interface_call_log_body` (
  `log_id` bigint NOT NULL COMMENT '对应 bp_interface_call_log.id',
  `response_body` mediumtext COMMENT '响应内容（脱敏+截断后）',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '冗余创建时间，用于与主表同保留期清理',
  PRIMARY KEY (`log_id`),
  KEY `idx_bp_call_log_body_created` (`created_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='调用日志响应体独立存储（大字段外移，详情页点查）';


-- ============================================================
-- Migration: 000108_call_log_dedup_client_ip_index.up.sql
-- ============================================================

-- 000108: drop the fully-duplicate client_ip secondary index.
--
-- idx_bp_interface_call_log_client_ip_time (client_ip, created_at) is a
-- byte-for-byte duplicate of idx_bp_call_log_client_ip_created
-- (client_ip, created_at), added later by 000085. Keeping both means every
-- INSERT and every row DELETE maintains two identical B-tree entries, doubling
-- the index-maintenance redo/undo and page writes on the hottest table in the
-- system. Dropping the older one is safe: MySQL chooses either index for the
-- same query shape, and the newer name is the one referenced by EXPLAIN
-- baselines.
--
-- DROP INDEX on a large table requires a brief metadata-lock and an index
-- rebuild; run this in a low-write window. It does not touch data.

ALTER TABLE `bp_interface_call_log`
  DROP INDEX `idx_bp_interface_call_log_client_ip_time`;


-- ============================================================
-- Migration: 000109_account_ban_full_suspension.up.sql
-- ============================================================

-- 账号封禁 = 全量封禁：封禁时把名下启用中的接口 Key 一并冻结。
--
-- 为什么在 Key 行上再落一个状态，而不是只靠 bp_user.status 判定：
--   1) 网关只判 user.status 时，Key 列表仍显示「启用」，运营无法从数据上确认
--      该账号的调用凭证已失效；两处状态不一致正是上一轮「封禁了但 key 还能用」
--      的观感来源。
--   2) 任何新增的鉴权入口（未来的平面、离线导出、按 Key 维度的白名单下发）
--      只要读了 bp_api_key 就可能漏判 user.status；把暂停落到 Key 行是纵深防御。
--   3) 解封需要精确恢复「因封禁而停」的 Key，不能连带把用户自己主动禁用的
--      Key 也打开，所以必须有独立的标记位。
--
-- suspended_by_ban 语义：
--   0 = 未因封禁暂停（正常行，含用户主动禁用）
--   1 = 因临时封禁暂停；解封时自动恢复为启用
--   2 = 因永久封禁作废；解封后仍保持禁用，需管理员在 Key 管理里手动启用
ALTER TABLE `bp_api_key`
  ADD COLUMN `suspended_by_ban` tinyint NOT NULL DEFAULT 0
    COMMENT '封禁联动：0=未暂停 1=临时封禁暂停(解封恢复) 2=永久封禁作废(不自动恢复)'
    AFTER `status`;

-- 历史数据对齐：本次变更之前封禁的账号，其 Key 仍是 status=1。
-- 只处理「当前确实处于封禁态」的账号，判断式与 accountstatus.EffectiveStatusSQL
-- 完全一致：status=0 且不是「临时封禁已到期」。已到期的临时封禁账号即将由
-- 到期解封任务恢复，不能在这里被误冻结。
-- 永久封禁记 2（解封后也不自动恢复），临时封禁记 1（解封即恢复）。
UPDATE `bp_api_key` k
  INNER JOIN `bp_user` u ON u.id = k.user_id
   SET k.status = 0,
       k.suspended_by_ban = IF(u.ban_permanent = 1, 2, 1)
 WHERE u.status = 0
   AND NOT (u.ban_permanent = 0
            AND u.ban_until IS NOT NULL
            AND u.ban_until <= NOW())
   AND k.status = 1;


-- ============================================================
-- Migration: 000110_call_outcome_classification.up.sql
-- ============================================================

-- Call outcomes are now classified per row instead of being inferred from the
-- single success flag at read time. outcome_class is written by the gateway:
--   0 = unclassified (legacy rows; statistics derive the class from status)
--   1 = SUCCESS
--   2 = EXPECTED_EMPTY (HTTP 404 or a configured business stop code)
--   3 = CLIENT_ERROR
--   4 = THROTTLE
--   5 = UPSTREAM_ERROR
-- Keeping 0 as "derive at read time" avoids a full-table rewrite on a large
-- append-only log; every new row writes an explicit class.
ALTER TABLE `bp_interface_call_log`
  ADD COLUMN `outcome_class` tinyint NOT NULL DEFAULT 0 COMMENT '调用结果分类 0=按状态推导 1=成功 2=预期空结果 3=客户端错误 4=限流 5=上游失败';

-- The three minute-layer projections carry the outcome buckets used by the
-- console composition card and the SLO alerts. Existing rows are rebuilt by
-- the analytics worker through the durable repair task below; until that
-- finishes, readers report the composition as unavailable instead of showing
-- a partial denominator as complete.
ALTER TABLE `bp_call_metric_minute`
  ADD COLUMN `expected_empty_count` bigint unsigned NOT NULL DEFAULT 0 COMMENT '预期空结果数（404/配置业务码）',
  ADD COLUMN `client_error_count` bigint unsigned NOT NULL DEFAULT 0 COMMENT '客户端错误数（4xx，不含404/429）',
  ADD COLUMN `throttle_count` bigint unsigned NOT NULL DEFAULT 0 COMMENT '限流数（429）',
  ADD COLUMN `upstream_error_count` bigint unsigned NOT NULL DEFAULT 0 COMMENT '上游失败数（5xx/网络/超时/2xx业务失败）';

ALTER TABLE `bp_call_user_metric_minute`
  ADD COLUMN `expected_empty_count` bigint unsigned NOT NULL DEFAULT 0 COMMENT '预期空结果数（404/配置业务码）',
  ADD COLUMN `client_error_count` bigint unsigned NOT NULL DEFAULT 0 COMMENT '客户端错误数（4xx，不含404/429）',
  ADD COLUMN `throttle_count` bigint unsigned NOT NULL DEFAULT 0 COMMENT '限流数（429）',
  ADD COLUMN `upstream_error_count` bigint unsigned NOT NULL DEFAULT 0 COMMENT '上游失败数（5xx/网络/超时/2xx业务失败）';

ALTER TABLE `bp_call_api_metric_minute`
  ADD COLUMN `expected_empty_count` bigint unsigned NOT NULL DEFAULT 0 COMMENT '预期空结果数（404/配置业务码）',
  ADD COLUMN `client_error_count` bigint unsigned NOT NULL DEFAULT 0 COMMENT '客户端错误数（4xx，不含404/429）',
  ADD COLUMN `throttle_count` bigint unsigned NOT NULL DEFAULT 0 COMMENT '限流数（429）',
  ADD COLUMN `upstream_error_count` bigint unsigned NOT NULL DEFAULT 0 COMMENT '上游失败数（5xx/网络/超时/2xx业务失败）';

-- Backfill only the three minute projections over the last day. The production
-- call log is tens of GB, so a broad 3-day/all-projection repair would hold the
-- analytics worker for hours. Bits 0/1/3 are metric-minute, api-minute and
-- user-minute; the failure_count predicate is unchanged, so no other projection
-- needs rebuilding. The repair is bounded by the dataset floor and is
-- idempotent: it clears and replays the same minute buckets.
INSERT INTO `bp_analytics_checkpoint`
  (`worker_key`,`repair_status`,`repair_window_days`,`repair_projection_mask`,
   `repair_phase`,`repair_clear_projection`)
VALUES
  ('call_log_rollup','PENDING',1,11,'IDLE',0)
ON DUPLICATE KEY UPDATE
  `repair_window_days`=GREATEST(COALESCE(`repair_window_days`,0),1),
  `repair_projection_mask`=(COALESCE(`repair_projection_mask`,0) | 11),
  `repair_status`='PENDING',
  `repair_phase`='IDLE',
  `repair_requested_at`=NOW(6),
  `repair_started_at`=NULL,
  `repair_completed_at`=NULL,
  `repair_upper_log_id`=0,
  `repair_cursor_id`=0,
  `repair_window_start`=NULL,
  `repair_clear_projection`=0;


-- ============================================================
-- Migration: 000111_oauth_login.up.sql
-- ============================================================

-- Third-party login providers.
--
-- Provider configuration is data-driven so adding a standard OAuth2/OIDC
-- provider does not require a backend deploy. The legacy QQ config table is
-- intentionally kept for one release: its values are copied into the `qq`
-- provider row below, but the provider stays disabled until an administrator
-- verifies the redirect URI and enables it.

CREATE TABLE IF NOT EXISTS `bp_auth_oauth_config` (
  `id` tinyint NOT NULL DEFAULT 1,
  `frontend_base_url` varchar(1024) NOT NULL DEFAULT '' COMMENT '前端基础地址，登录回调后跳转使用',
  `register_ip_daily_limit` int NOT NULL DEFAULT 10 COMMENT '第三方注册每 IP 每日上限，0=不限制',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  CONSTRAINT `chk_bp_auth_oauth_config_singleton` CHECK (`id` = 1)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='第三方登录全局配置';

INSERT INTO `bp_auth_oauth_config` (`id`)
VALUES (1)
ON DUPLICATE KEY UPDATE `id` = `id`;

CREATE TABLE IF NOT EXISTS `bp_auth_oauth_provider` (
  `id` bigint NOT NULL AUTO_INCREMENT,
  `provider_key` varchar(32) NOT NULL COMMENT '对外唯一标识，如 qq/wechat/github/linuxdo',
  `display_name` varchar(64) NOT NULL,
  `icon_key` varchar(32) NOT NULL DEFAULT 'generic',
  `driver` varchar(32) NOT NULL COMMENT 'qq/wechat/github/oauth2/oidc',
  `provider_group` varchar(32) NOT NULL DEFAULT '' COMMENT '同厂商归并，用于 unionid 兜底',
  `enabled` tinyint NOT NULL DEFAULT 0,
  `register_mode` varchar(16) NOT NULL DEFAULT 'off' COMMENT 'off/confirm/auto',
  `link_by_email` tinyint NOT NULL DEFAULT 0 COMMENT '是否允许按已验证邮箱合并，默认关闭',
  `require_verified_email` tinyint NOT NULL DEFAULT 0,
  `client_id` varchar(255) NOT NULL DEFAULT '',
  `client_secret` varchar(1024) NOT NULL DEFAULT '' COMMENT 'secretbox 加密存储',
  `issuer` varchar(512) NOT NULL DEFAULT '',
  `authorization_endpoint` varchar(1024) NOT NULL DEFAULT '',
  `token_endpoint` varchar(1024) NOT NULL DEFAULT '',
  `userinfo_endpoint` varchar(1024) NOT NULL DEFAULT '',
  `jwks_uri` varchar(1024) NOT NULL DEFAULT '',
  `scopes` varchar(512) NOT NULL DEFAULT '',
  `redirect_uri` varchar(1024) NOT NULL DEFAULT '' COMMENT '必须与平台后台完全一致',
  `extra_config` json DEFAULT NULL COMMENT '字段映射、token 认证方式等',
  `sort_no` int NOT NULL DEFAULT 0,
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_bp_auth_oauth_provider_key` (`provider_key`),
  KEY `idx_bp_auth_oauth_provider_enabled` (`enabled`, `sort_no`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='第三方登录 Provider 配置';

INSERT INTO `bp_auth_oauth_provider`
  (`provider_key`, `display_name`, `icon_key`, `driver`, `provider_group`, `enabled`, `register_mode`, `scopes`, `issuer`, `authorization_endpoint`, `token_endpoint`, `userinfo_endpoint`, `sort_no`)
VALUES
  ('qq', 'QQ', 'qq', 'qq', 'qq_connect', 0, 'confirm', 'get_user_info', '', '', '', '', 10),
  ('wechat', '微信', 'wechat', 'wechat', 'wechat_open', 0, 'confirm', 'snsapi_login', '', '', '', '', 20),
  ('github', 'GitHub', 'github', 'github', 'github', 0, 'confirm', 'read:user user:email', '', 'https://github.com/login/oauth/authorize', 'https://github.com/login/oauth/access_token', 'https://api.github.com/user', 30),
  ('linuxdo', 'LinuxDo', 'linuxdo', 'oauth2', 'linuxdo', 0, 'confirm', '', 'https://linux.do', 'https://connect.linux.do/oauth2/authorize', 'https://connect.linux.do/oauth2/token', 'https://connect.linux.do/api/user', 40)
ON DUPLICATE KEY UPDATE
  `display_name` = VALUES(`display_name`),
  `icon_key` = VALUES(`icon_key`),
  `driver` = VALUES(`driver`),
  `provider_group` = VALUES(`provider_group`),
  `scopes` = VALUES(`scopes`),
  `issuer` = CASE WHEN `issuer` = '' THEN VALUES(`issuer`) ELSE `issuer` END,
  `authorization_endpoint` = CASE WHEN `authorization_endpoint` = '' THEN VALUES(`authorization_endpoint`) ELSE `authorization_endpoint` END,
  `token_endpoint` = CASE WHEN `token_endpoint` = '' THEN VALUES(`token_endpoint`) ELSE `token_endpoint` END,
  `userinfo_endpoint` = CASE WHEN `userinfo_endpoint` = '' THEN VALUES(`userinfo_endpoint`) ELSE `userinfo_endpoint` END,
  `sort_no` = VALUES(`sort_no`);

-- LinuxDo is a standard OAuth2 service with a custom userinfo payload. The
-- mapping keeps the driver generic: another provider only needs a new row.
UPDATE `bp_auth_oauth_provider`
SET `extra_config` = JSON_OBJECT(
  'subject_field', 'id',
  'username_field', 'username',
  'nickname_field', 'name',
  'avatar_field', 'avatar_template',
  'email_field', 'email',
  'email_verified_field', 'email_verified',
  'trust_level_field', 'trust_level',
  'active_field', 'active',
  'silenced_field', 'silenced',
  'token_auth_style', 'client_secret_post',
  'userinfo_auth_style', 'bearer',
  'avatar_size', 96,
  'pkce', FALSE
)
WHERE `provider_key` = 'linuxdo' AND `extra_config` IS NULL;

CREATE TABLE IF NOT EXISTS `bp_auth_oauth_identity` (
  `id` bigint NOT NULL AUTO_INCREMENT,
  `user_id` bigint NOT NULL,
  `provider_key` varchar(32) NOT NULL,
  `provider_group` varchar(32) NOT NULL DEFAULT '',
  `subject` varchar(255) NOT NULL COMMENT 'openid / GitHub id / LinuxDo id',
  `union_id` varchar(255) NOT NULL DEFAULT '',
  `nickname` varchar(128) NOT NULL DEFAULT '',
  `avatar` varchar(1024) NOT NULL DEFAULT '',
  `email` varchar(255) NOT NULL DEFAULT '',
  `email_verified` tinyint NOT NULL DEFAULT 0,
  `bound_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `last_login_at` datetime DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_bp_auth_oauth_identity_subject` (`provider_key`, `subject`),
  UNIQUE KEY `uk_bp_auth_oauth_identity_user_provider` (`user_id`, `provider_key`),
  KEY `idx_bp_auth_oauth_identity_union` (`provider_group`, `union_id`),
  KEY `idx_bp_auth_oauth_identity_user` (`user_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='第三方身份绑定';

ALTER TABLE `bp_user`
  ADD COLUMN `password_set` tinyint NOT NULL DEFAULT 1 COMMENT '是否设置过密码；OAuth 自动建号为 0' AFTER `password_hash`;

-- Copy the legacy QQ scaffold without enabling it. client_secret is copied as
-- stored: secretbox.DecryptCompatible accepts both legacy plaintext and the
-- current enc:v1 ciphertext.
UPDATE `bp_auth_oauth_provider` p
JOIN `bp_auth_qq_login_config` q ON q.id = 1
SET
  p.client_id = CASE WHEN p.client_id = '' THEN q.app_id ELSE p.client_id END,
  p.client_secret = CASE WHEN p.client_secret = '' THEN q.app_key ELSE p.client_secret END,
  p.redirect_uri = CASE WHEN p.redirect_uri = '' THEN q.redirect_uri ELSE p.redirect_uri END
WHERE p.provider_key = 'qq';

UPDATE `bp_auth_oauth_config` c
JOIN `bp_auth_qq_login_config` q ON q.id = 1
SET c.frontend_base_url = CASE WHEN c.frontend_base_url = '' THEN q.frontend_base_url ELSE c.frontend_base_url END
WHERE c.id = 1;

-- Reuse the existing "quick login" menu entry instead of creating a duplicate
-- route. Role grants are keyed by menu id, so they survive the code rename.
UPDATE `bp_menu`
SET `name` = 'SystemOAuthLoginConfig',
    `auth_code` = 'system:login:oauth-config',
    `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.title', '第三方登录', '$.icon', 'mdi:login-variant'),
    `status` = 1
WHERE `id` = 158;

UPDATE `bp_menu`
SET `name` = 'SystemOAuthLoginConfigUpdate',
    `auth_code` = 'system:login:oauth-config:update',
    `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.title', '保存第三方登录设置'),
    `status` = 1
WHERE `id` = 159;

INSERT IGNORE INTO `bp_role_menu` (`role_id`, `menu_id`)
SELECT 1, `id` FROM `bp_menu` WHERE `id` IN (158, 159);


-- ============================================================
-- Migration: 000112_unified_registration_policy.up.sql
-- ============================================================

-- Unified registration policy.
--
-- The local registration page and the third-party login page used to own
-- separate registration switches and limits. From 1.2.46 the register config
-- is the single global policy source; every provider may override role, gift
-- points, IP limits, verified-email policy and provider risk gates.

ALTER TABLE `bp_auth_register_config`
  ADD COLUMN `register_ip_day_limit` int NOT NULL DEFAULT 0
    COMMENT '本地注册每 IP 每日上限，0=不限制' AFTER `register_ip_hour_limit`,
  ADD COLUMN `oauth_register_enabled` tinyint NOT NULL DEFAULT 1
    COMMENT '第三方注册总开关' AFTER `register_enabled`,
  ADD COLUMN `oauth_register_mode_default` varchar(16) NOT NULL DEFAULT 'confirm'
    COMMENT '第三方注册默认模式 off/confirm/auto' AFTER `oauth_register_enabled`,
  ADD COLUMN `oauth_register_ip_hour_limit` int NOT NULL DEFAULT 0
    COMMENT '第三方注册每 IP 每小时上限，0=不限制' AFTER `register_ip_day_limit`,
  ADD COLUMN `oauth_register_ip_day_limit` int NOT NULL DEFAULT 10
    COMMENT '第三方注册每 IP 每日上限，0=不限制' AFTER `oauth_register_ip_hour_limit`,
  ADD COLUMN `oauth_register_ip_max_accounts` int NOT NULL DEFAULT 0
    COMMENT '第三方注册每 IP 每平台终身上限，0=不限制' AFTER `oauth_register_ip_day_limit`,
  ADD COLUMN `oauth_require_verified_email` tinyint NOT NULL DEFAULT 0
    COMMENT '第三方注册是否默认要求已验证邮箱' AFTER `oauth_register_ip_max_accounts`;

-- Preserve the value from the old third-party global config. The old column is
-- kept for one release so a rollback to 1.2.45 still has its input.
UPDATE `bp_auth_register_config` c
JOIN `bp_auth_oauth_config` o ON o.id = 1
SET c.oauth_register_ip_day_limit = o.register_ip_daily_limit
WHERE c.id = 1;

ALTER TABLE `bp_auth_oauth_provider`
  ADD COLUMN `default_role_id` bigint NOT NULL DEFAULT 0
    COMMENT '注册默认角色，0=跟随全局' AFTER `register_mode`,
  ADD COLUMN `register_gift_points` bigint NOT NULL DEFAULT -1
    COMMENT '注册赠送点数，-1=跟随全局' AFTER `default_role_id`,
  ADD COLUMN `register_ip_hour_limit` int NOT NULL DEFAULT -1
    COMMENT '每 IP 每小时注册上限，-1=跟随全局' AFTER `register_gift_points`,
  ADD COLUMN `register_ip_day_limit` int NOT NULL DEFAULT -1
    COMMENT '每 IP 每日注册上限，-1=跟随全局' AFTER `register_ip_hour_limit`,
  ADD COLUMN `register_ip_max_accounts` int NOT NULL DEFAULT -1
    COMMENT '每 IP 每平台终身上限，-1=跟随全局' AFTER `register_ip_day_limit`,
  ADD COLUMN `require_verified_email_mode` tinyint NOT NULL DEFAULT -1
    COMMENT '是否要求已验证邮箱：-1=跟随全局，0=不要求，1=要求' AFTER `require_verified_email`,
  ADD COLUMN `min_account_age_days` int NOT NULL DEFAULT 0
    COMMENT '第三方账号最小注册天数，0=不限制' AFTER `require_verified_email_mode`,
  ADD COLUMN `min_trust_level` int NOT NULL DEFAULT 0
    COMMENT '第三方平台最低信任等级，0=不限制' AFTER `min_account_age_days`,
  ADD COLUMN `require_active` tinyint NOT NULL DEFAULT 1
    COMMENT '要求第三方账号处于活跃状态' AFTER `min_trust_level`,
  ADD COLUMN `reject_silenced` tinyint NOT NULL DEFAULT 1
    COMMENT '拒绝被禁言/受限的第三方账号' AFTER `require_active`;

UPDATE `bp_auth_oauth_provider`
SET `require_verified_email_mode` = CASE WHEN `require_verified_email` = 1 THEN 1 ELSE -1 END;

-- Per-channel lifetime registration quota. The existing bp_register_ip_used
-- remains local-only for backward compatibility; OAuth uses this counter so
-- its lifetime cap can be enforced atomically per provider.
CREATE TABLE IF NOT EXISTS `bp_register_ip_usage` (
  `ip_hash` binary(32) NOT NULL,
  `channel` varchar(64) NOT NULL COMMENT 'local 或 oauth:<provider_key>',
  `used` int NOT NULL DEFAULT 0,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`ip_hash`, `channel`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='注册 IP 分渠道累计配额（keyed hash，无明文 IP）';


-- ============================================================
-- Migration: 000113_user_inbox.up.sql
-- ============================================================

-- 站内消息中心（全站对用户的通知）。
--
-- 管理员可向全部用户或指定用户发送站内消息；每用户一行，便于各自的
-- 已读状态。可选同时通过统一邮件 outbox 向绑定邮箱的用户发送邮件副本。

CREATE TABLE IF NOT EXISTS `bp_user_inbox` (
  `id` bigint NOT NULL AUTO_INCREMENT,
  `user_id` bigint NOT NULL COMMENT '接收用户',
  `title` varchar(128) NOT NULL,
  `body` varchar(4000) NOT NULL DEFAULT '' COMMENT '纯文本正文',
  `kind` varchar(32) NOT NULL DEFAULT 'SYSTEM' COMMENT 'SYSTEM=管理员群发',
  `is_read` tinyint NOT NULL DEFAULT 0,
  `read_at` datetime DEFAULT NULL,
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  KEY `idx_bp_user_inbox_user` (`user_id`, `is_read`, `id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='站内消息';

-- 管理端：全站通知发送页
INSERT INTO `bp_menu`
  (`id`, `pid`, `name`, `path`, `component`, `redirect`, `type`, `auth_code`, `meta`, `status`)
VALUES
  (241, 30, 'SystemInboxBroadcast', '/system/inbox-broadcast', '/system/inbox-broadcast/index', NULL, 'menu', 'system:inbox:broadcast', JSON_OBJECT('title', 'inbox.broadcastTitle', 'icon', 'lucide:megaphone', 'order', 9), 1)
ON DUPLICATE KEY UPDATE
  `pid` = VALUES(`pid`),
  `path` = VALUES(`path`),
  `component` = VALUES(`component`),
  `redirect` = VALUES(`redirect`),
  `type` = VALUES(`type`),
  `auth_code` = VALUES(`auth_code`),
  `meta` = VALUES(`meta`),
  `status` = VALUES(`status`);

-- 用户端：消息中心
INSERT INTO `bp_menu`
  (`id`, `pid`, `name`, `path`, `component`, `redirect`, `type`, `auth_code`, `meta`, `status`)
VALUES
  (242, 84, 'UserInbox', '/user/inbox', '/user/inbox/index', NULL, 'menu', 'inbox:self:read', JSON_OBJECT('title', 'inbox.userTitle', 'icon', 'lucide:bell', 'order', 9), 1)
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
SELECT 1, `id` FROM `bp_menu` WHERE `id` = 241;

INSERT IGNORE INTO `bp_role_menu` (`role_id`, `menu_id`)
SELECT `id`, 242 FROM `bp_role` WHERE `role_key` IN ('admin', 'user');


-- ============================================================
-- Migration: 000114_package_user_management.up.sql
-- ============================================================

-- 用户套餐管理：管理员查看、启用/禁用、撤销单个用户的套餐权益。

-- 管理端：用户套餐页（挂在「套餐管理」目录下）
INSERT INTO `bp_menu`
  (`id`, `pid`, `name`, `path`, `component`, `redirect`, `type`, `auth_code`, `meta`, `status`)
VALUES
  (243, 70, 'UserPackageList', '/package/user', '/package/user/list', NULL, 'menu', 'package:user:list', JSON_OBJECT('title', 'system.package.userTitle', 'order', 5), 1)
ON DUPLICATE KEY UPDATE
  `pid` = VALUES(`pid`),
  `path` = VALUES(`path`),
  `component` = VALUES(`component`),
  `redirect` = VALUES(`redirect`),
  `type` = VALUES(`type`),
  `auth_code` = VALUES(`auth_code`),
  `meta` = VALUES(`meta`),
  `status` = VALUES(`status`);

-- 按钮权限：启用/禁用用户权益、撤销用户权益
INSERT INTO `bp_menu`
  (`id`, `pid`, `name`, `path`, `component`, `redirect`, `type`, `auth_code`, `meta`, `status`)
VALUES
  (244, 243, 'UserPackageUpdate', '', NULL, NULL, 'button', 'package:user:update', JSON_OBJECT('title', 'system.package.userUpdateTitle'), 1),
  (245, 243, 'UserPackageDelete', '', NULL, NULL, 'button', 'package:user:delete', JSON_OBJECT('title', 'system.package.userDeleteTitle'), 1)
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
SELECT 1, `id` FROM `bp_menu` WHERE `id` IN (243, 244, 245);


-- ============================================================
-- Migration: 000115_payment_expiry_package_limits.up.sql
-- ============================================================

-- 支付订单时效与套餐限量：
-- 1) 订单增加库存占用状态，用于限量套餐预占/释放/成交追溯；
-- 2) 套餐增加限量份数、每人限购与售卖时间窗口；
-- 3) 增加待支付订单复用与过期扫描索引。

ALTER TABLE `bp_package_global`
  ADD COLUMN `stock_limit` int NOT NULL DEFAULT 0 COMMENT '限量份数，0不限' AFTER `qps_limit`,
  ADD COLUMN `stock_used` int NOT NULL DEFAULT 0 COMMENT '已占用份数（含待支付预占）' AFTER `stock_limit`,
  ADD COLUMN `per_user_limit` int NOT NULL DEFAULT 0 COMMENT '每人限购份数，0不限' AFTER `stock_used`,
  ADD COLUMN `sale_start_at` datetime DEFAULT NULL COMMENT '开售时间，空表示不限' AFTER `per_user_limit`,
  ADD COLUMN `sale_end_at` datetime DEFAULT NULL COMMENT '停售时间，空表示不限' AFTER `sale_start_at`,
  ADD KEY `idx_bp_package_global_sale` (`status`, `sale_start_at`, `sale_end_at`);

ALTER TABLE `bp_package_point`
  ADD COLUMN `stock_limit` int NOT NULL DEFAULT 0 COMMENT '限量份数，0不限' AFTER `points`,
  ADD COLUMN `stock_used` int NOT NULL DEFAULT 0 COMMENT '已占用份数（含待支付预占）' AFTER `stock_limit`,
  ADD COLUMN `per_user_limit` int NOT NULL DEFAULT 0 COMMENT '每人限购份数，0不限' AFTER `stock_used`,
  ADD COLUMN `sale_start_at` datetime DEFAULT NULL COMMENT '开售时间，空表示不限' AFTER `per_user_limit`,
  ADD COLUMN `sale_end_at` datetime DEFAULT NULL COMMENT '停售时间，空表示不限' AFTER `sale_start_at`,
  ADD KEY `idx_bp_package_point_sale` (`status`, `sale_start_at`, `sale_end_at`);

ALTER TABLE `bp_package_interface_spec`
  ADD COLUMN `stock_limit` int NOT NULL DEFAULT 0 COMMENT '限量份数，0不限' AFTER `qps_limit`,
  ADD COLUMN `stock_used` int NOT NULL DEFAULT 0 COMMENT '已占用份数（含待支付预占）' AFTER `stock_limit`,
  ADD COLUMN `per_user_limit` int NOT NULL DEFAULT 0 COMMENT '每人限购份数，0不限' AFTER `stock_used`,
  ADD COLUMN `sale_start_at` datetime DEFAULT NULL COMMENT '开售时间，空表示不限' AFTER `per_user_limit`,
  ADD COLUMN `sale_end_at` datetime DEFAULT NULL COMMENT '停售时间，空表示不限' AFTER `sale_start_at`,
  ADD KEY `idx_bp_package_interface_spec_sale` (`status`, `sale_start_at`, `sale_end_at`);

ALTER TABLE `bp_payment_order`
  ADD COLUMN `stock_reserved` tinyint NOT NULL DEFAULT 0 COMMENT '库存占用状态：0不占用,1占用中,2已释放,3已成交' AFTER `grant_points`,
  ADD KEY `idx_bp_payment_order_reuse` (`user_id`, `order_type`, `target_id`, `status`, `id`),
  ADD KEY `idx_bp_payment_order_expire` (`status`, `expire_at`);


-- ============================================================
-- Migration: 000116_i18n_dictionary.up.sql
-- ============================================================

-- 后端业务数据多语言翻译字典。
--
-- 默认语言原文仍保存在各业务表；本表只保存非默认语言的覆盖译文。
-- 读取接口按请求语言回填，未翻译字段回退默认语言原文。

CREATE TABLE IF NOT EXISTS `bp_i18n_locale` (
  `code` varchar(16) NOT NULL COMMENT 'BCP-47 语言码，如 zh-CN / en-US',
  `label` varchar(64) NOT NULL COMMENT '语言自称，如 简体中文 / English',
  `fallback_code` varchar(16) DEFAULT NULL COMMENT '缺失时的回退语言码',
  `enabled` tinyint NOT NULL DEFAULT 1 COMMENT '是否对外启用',
  `is_default` tinyint DEFAULT NULL COMMENT '1=默认语言；NULL=非默认',
  `sort_no` int NOT NULL DEFAULT 0 COMMENT '语言选择器排序',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`code`),
  UNIQUE KEY `uk_bp_i18n_locale_default` (`is_default`),
  KEY `idx_bp_i18n_locale_enabled` (`enabled`, `sort_no`, `code`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='启用语言与回退关系';

INSERT INTO `bp_i18n_locale` (`code`,`label`,`fallback_code`,`enabled`,`is_default`,`sort_no`)
VALUES
  ('zh-CN','简体中文',NULL,1,1,0),
  ('en-US','English','zh-CN',1,NULL,1)
ON DUPLICATE KEY UPDATE
  `label` = VALUES(`label`),
  `fallback_code` = VALUES(`fallback_code`),
  `enabled` = VALUES(`enabled`),
  `is_default` = VALUES(`is_default`),
  `sort_no` = VALUES(`sort_no`);

CREATE TABLE IF NOT EXISTS `bp_i18n_entry` (
  `id` bigint NOT NULL AUTO_INCREMENT,
  `resource_type` varchar(64) NOT NULL COMMENT '资源类型，如 interface / notice / site',
  `resource_key` varchar(191) NOT NULL COMMENT '资源稳定主键，如 42 / 1',
  `field` varchar(64) NOT NULL COMMENT '注册表字段名，如 name / contentHtml',
  `locale` varchar(16) NOT NULL COMMENT '目标语言码',
  `value` mediumtext NOT NULL COMMENT '译文；富文本字段已净化',
  `source_hash` char(64) DEFAULT NULL COMMENT '保存译文时的原文 SHA-256',
  `updated_by` bigint DEFAULT NULL COMMENT '最后编辑人',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_bp_i18n_entry` (`resource_type`,`resource_key`,`field`,`locale`),
  KEY `idx_bp_i18n_entry_locale_type` (`locale`,`resource_type`),
  KEY `idx_bp_i18n_entry_resource` (`resource_type`,`resource_key`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='后端业务数据翻译字典';

-- 管理端：系统设置 → 翻译字典
INSERT INTO `bp_menu`
  (`id`,`pid`,`name`,`path`,`component`,`redirect`,`type`,`auth_code`,`meta`,`status`)
VALUES
  (250, 30, 'SystemTranslation', '/system/translation', '/system/translation/index', NULL, 'menu', 'system:i18n:list', JSON_OBJECT('title','translationAdmin.title','icon','mdi:translate','order',11), 1),
  (251, 250, 'SystemTranslationUpdate', '', NULL, NULL, 'button', 'system:i18n:update', JSON_OBJECT('title','translationAdmin.permissions.update','order',1), 1)
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
SELECT 1, `id` FROM `bp_menu` WHERE `id` IN (250, 251);


-- ============================================================
-- Migration: 000117_friend_link_hub.up.sql
-- ============================================================

-- 友链三合一：「友情链接」「友链中心」「友链配置」原来是三个菜单入口，其中
-- 友链配置只是把友链中心重渲染一遍。这里把 138 升级为唯一可见入口（三个页签：
-- 申请审核 / 友链列表 / 站点配置），133 与 140 保留为隐藏的别名路由。
--
-- 不删除 133/140 的行：菜单权限码来自 bp_menu.auth_code 与角色授权，删除会让
-- 已被单独授权「友情链接」或「友链配置」的角色直接丢失 friend-link:list /
-- friend-link:config:update，页面按钮随之失效。

UPDATE `bp_menu`
SET `name` = 'FriendLinkCenter',
    `path` = '/content/friend-link',
    `component` = '/friend-link',
    `redirect` = NULL,
    `type` = 'menu',
    `auth_code` = 'friend-link:application:list',
    `meta` = JSON_SET(
      COALESCE(`meta`, JSON_OBJECT()),
      '$.title', 'system.friendLink.applicationTitle',
      '$.icon', 'mdi:link-variant',
      '$.order', 4
    ),
    `status` = 1
WHERE `id` = 138 AND `name` = 'FriendLinkApplications';

UPDATE `bp_menu`
SET `component` = '/friend-link',
    `meta` = JSON_SET(
      COALESCE(`meta`, JSON_OBJECT()),
      '$.hideInMenu', true,
      '$.navigationCleanup', '000117-friend-link-hub'
    ),
    `status` = 1
WHERE `id` IN (133, 140);

-- 能看到任意一个友链入口的角色，都能看到合并后的友链中心。
INSERT IGNORE INTO `bp_role_menu` (`role_id`, `menu_id`)
SELECT `role_id`, 138
FROM `bp_role_menu`
WHERE `menu_id` IN (133, 140);


-- ============================================================
-- Migration: 000118_friend_link_self.up.sql
-- ============================================================

-- 友链自助管理的数据基础。
--
-- 1) bp_friend_link 此前只存 applicant_name/applicant_email 文本，没有归属账号，
--    所以「用户管自己的友链」无从查起。审核通过时已经写入了
--    source_application_id，历史数据据此回填 user_id；管理员手工新增的友链
--    保持 user_id=0（不属于任何用户）。
ALTER TABLE `bp_friend_link`
  ADD COLUMN `user_id` bigint NOT NULL DEFAULT 0 COMMENT 'Owning account id, 0 = added by admin' AFTER `applicant_email`,
  ADD KEY `idx_bp_friend_link_user` (`user_id`, `status`, `id`);

UPDATE `bp_friend_link` l
JOIN `bp_friend_link_application` a ON a.`id` = l.`source_application_id`
SET l.`user_id` = a.`user_id`
WHERE l.`user_id` = 0 AND a.`user_id` > 0;

-- 2) 用户改已上线的友链不能直接生效，走同一张申请表的变更请求：
--    NEW=首次申请（既有链路），EDIT=修改信息，REMOVE=申请下架。
--    审核通过时才把请求里的新值落到 bp_friend_link，因此审核列表、驳回模板、
--    邮件通知与审计全部复用现有链路，不再新增一套并行的审核状态。
ALTER TABLE `bp_friend_link_application`
  ADD COLUMN `link_id` bigint NOT NULL DEFAULT 0 COMMENT 'Target live link id for EDIT/REMOVE' AFTER `user_id`,
  ADD COLUMN `change_kind` varchar(16) NOT NULL DEFAULT 'NEW' COMMENT 'NEW/EDIT/REMOVE' AFTER `link_id`,
  ADD KEY `idx_bp_friend_link_app_link` (`link_id`, `status`);


-- ============================================================
-- Migration: 000119_request_quota_and_register_risk.up.sql
-- ============================================================

-- 请求配额与注册防刷（2026-11）。
--
-- 1) bp_role 增加「阶梯式请求配额」：分 / 时 / 日 / 月 四层上限，
--    由 user.primary_role_id 决定生效配置，因此用户组即配额档位。
--    request_quota_count_mode 控制计量口径：
--      attempt = 从请求进入配额闸门开始计量，失败也计；
--      success = 仅成功返回的调用计量。
--    默认全部关闭（request_quota_enabled=0）；管理员在角色管理里按组启用。
--
-- 2) bp_auth_register_config 增加注册风控参数：
--    新账号首日调用上限、设备/网段日注册上限、评分阈值、影子模式。
--    默认首日额度 0（不限），风险评分开启但处于影子模式：只记录不拒绝/不隔离。
--
-- 3) bp_register_event 留存每一次注册决策（allow/quarantine/reject），
--    用于调参与取证；所有 IP/设备只落 keyed hash，不存明文。
-- 4) bp_user_risk_state 承载「隔离观察」：评分达到隔离阈值的新账号
--    可正常登录，但 24h 内不能调用接口；到期自动恢复，无需 worker。
-- 5) bp_auth_email_code 增加 attempts，6 位验证码不再可无限试。
ALTER TABLE `bp_role`
  ADD COLUMN `request_quota_enabled` tinyint NOT NULL DEFAULT 0
    COMMENT 'Whether role request quota is enforced; 0 = unlimited' AFTER `self_register_enabled`,
  ADD COLUMN `request_quota_minute` bigint NOT NULL DEFAULT 60
    COMMENT 'Per-user request cap per minute; 0 = unlimited' AFTER `request_quota_enabled`,
  ADD COLUMN `request_quota_hour` bigint NOT NULL DEFAULT 600
    COMMENT 'Per-user request cap per hour; 0 = unlimited' AFTER `request_quota_minute`,
  ADD COLUMN `request_quota_day` bigint NOT NULL DEFAULT 1000
    COMMENT 'Per-user request cap per day; 0 = unlimited' AFTER `request_quota_hour`,
  ADD COLUMN `request_quota_month` bigint NOT NULL DEFAULT 20000
    COMMENT 'Per-user request cap per calendar month; 0 = unlimited' AFTER `request_quota_day`,
  ADD COLUMN `request_quota_count_mode` varchar(16) NOT NULL DEFAULT 'attempt'
    COMMENT 'attempt = count every admitted request, success = count successful calls only' AFTER `request_quota_month`,
  ADD CONSTRAINT `chk_bp_role_quota_count_mode`
    CHECK (`request_quota_count_mode` IN ('attempt', 'success')),
  ADD CONSTRAINT `chk_bp_role_quota_non_negative`
    CHECK (`request_quota_minute` >= 0 AND `request_quota_hour` >= 0
           AND `request_quota_day` >= 0 AND `request_quota_month` >= 0);

-- 默认所有用户组都不启用配额；需要时在「角色管理」逐个打开。
UPDATE `bp_role` SET `request_quota_enabled` = 0;

ALTER TABLE `bp_auth_register_config`
  ADD COLUMN `new_account_day_limit` bigint NOT NULL DEFAULT 0
    COMMENT 'Calls allowed during the first 24h after registration; 0 = unlimited' AFTER `register_ip_max_accounts`,
  ADD COLUMN `register_device_day_limit` int NOT NULL DEFAULT 5
    COMMENT 'Accounts allowed per signed device cookie per 24h; 0 = unlimited' AFTER `new_account_day_limit`,
  ADD COLUMN `register_ip_prefix_day_limit` int NOT NULL DEFAULT 10
    COMMENT 'Accounts allowed per IPv4 /24 or IPv6 /64 per 24h; 0 = unlimited' AFTER `register_device_day_limit`,
  ADD COLUMN `register_risk_enabled` tinyint NOT NULL DEFAULT 1
    COMMENT 'Enable registration risk scoring' AFTER `register_ip_prefix_day_limit`,
  ADD COLUMN `register_risk_shadow_mode` tinyint NOT NULL DEFAULT 1
    COMMENT '1 = record scores but never reject/quarantine' AFTER `register_risk_enabled`,
  ADD COLUMN `register_risk_quarantine_score` int NOT NULL DEFAULT 40
    COMMENT 'Score at or above which a new account is quarantined for 24h' AFTER `register_risk_shadow_mode`,
  ADD COLUMN `register_risk_reject_score` int NOT NULL DEFAULT 75
    COMMENT 'Score at or above which registration returns a fake success' AFTER `register_risk_quarantine_score`;

ALTER TABLE `bp_auth_email_code`
  ADD COLUMN `attempts` tinyint unsigned NOT NULL DEFAULT 0
    COMMENT 'Failed verification attempts; code is revoked at 5' AFTER `consumed_at`;

CREATE TABLE IF NOT EXISTS `bp_register_event` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `created_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `decision` varchar(16) NOT NULL COMMENT 'allow/quarantine/reject',
  `score` int NOT NULL DEFAULT 0 COMMENT 'Risk score 0-120',
  `user_id` bigint unsigned DEFAULT NULL COMMENT 'Only set when an account was created',
  `username` varchar(64) NOT NULL DEFAULT '',
  `email_domain` varchar(255) NOT NULL DEFAULT '',
  `client_ip_hash` binary(32) NOT NULL COMMENT 'iphash.RiskDigest(client ip)',
  `prefix_hash` binary(32) NOT NULL COMMENT 'iphash.PrefixDigest(client ip), /24 or /64',
  `device_hash` binary(32) NOT NULL COMMENT 'sha256(signed device cookie id)',
  `user_agent` varchar(512) NOT NULL DEFAULT '',
  `accept_language` varchar(128) NOT NULL DEFAULT '',
  `elapsed_ms` int NOT NULL DEFAULT 0 COMMENT 'Captcha issue to register submit',
  `signals` json DEFAULT NULL COMMENT 'Matched risk signals for tuning',
  PRIMARY KEY (`id`),
  KEY `idx_bp_register_event_created` (`created_at`),
  KEY `idx_bp_register_event_prefix` (`prefix_hash`, `created_at`),
  KEY `idx_bp_register_event_device` (`device_hash`, `created_at`),
  KEY `idx_bp_register_event_domain` (`email_domain`, `created_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='Registration decision stream (no plaintext IP)';

CREATE TABLE IF NOT EXISTS `bp_user_risk_state` (
  `user_id` bigint unsigned NOT NULL,
  `quarantine_until` datetime NOT NULL COMMENT 'API calls are rejected until this instant',
  `risk_score` int NOT NULL DEFAULT 0,
  `reason` varchar(255) NOT NULL DEFAULT '',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`user_id`),
  KEY `idx_bp_user_risk_state_until` (`quarantine_until`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='Account risk quarantine state';

-- 一批高置信度一次性/临时邮箱域。只补黑名单，不碰管理员配置的域名。
INSERT IGNORE INTO `bp_email_domain_rule` (`rule_kind`, `domain`) VALUES
  ('blacklist', '10minutemail.com'), ('blacklist', '1secmail.com'),
  ('blacklist', '234.ee'), ('blacklist', 'daodao.app'),
  ('blacklist', 'discard.email'), ('blacklist', 'dispostable.com'),
  ('blacklist', 'dropmail.me'), ('blacklist', 'emailondeck.com'),
  ('blacklist', 'fakeinbox.com'), ('blacklist', 'getnada.com'),
  ('blacklist', 'guerrillamail.com'), ('blacklist', '*.guerrillamail.com'),
  ('blacklist', 'inboxes.com'), ('blacklist', 'itime.day'),
  ('blacklist', 'linshiyou.com'), ('blacklist', 'lnovic.com'),
  ('blacklist', 'mail.tm'), ('blacklist', 'maildrop.cc'),
  ('blacklist', 'mailinator.com'), ('blacklist', '*.mailinator.com'),
  ('blacklist', 'mailnesia.com'), ('blacklist', 'mintemail.com'),
  ('blacklist', 'mohmal.com'), ('blacklist', 'moakt.com'),
  ('blacklist', 'sharklasers.com'), ('blacklist', 'spambox.us'),
  ('blacklist', 'temp-mail.org'), ('blacklist', '*.temp-mail.org'),
  ('blacklist', 'tempmail.com'), ('blacklist', 'tempr.email'),
  ('blacklist', 'textdiff.net'), ('blacklist', 'throwawaymail.com'),
  ('blacklist', 'trashmail.com'), ('blacklist', 'yopmail.com'),
  ('blacklist', '*.yopmail.com'), ('blacklist', 'youxiang.dev');


-- ============================================================
-- Migration: 000120_interface_quota.up.sql
-- ============================================================

-- 单接口维度的小时 / 天 / 周 / 月配额（2026-11-03）。
--
-- 与 bp_role 的「用户组总量配额」互补：
--   * bp_role 限制的是「一个账号在平台内的总调用量」；
--   * bp_interface_quota 限制的是「一个接口的单用户调用量」和「该接口的全平台
--     总调用量」，用于保护单个上游、控制单接口成本。
--
-- 0 表示不限制；enabled=0 时该接口不启用本层配额，完全保持原有行为。
-- count_mode 独立于用户组计量：attempt = 进入闸门即计（失败也计），
-- success = 成功返回后补记。这样即使某个用户组按成功计量，接口侧仍可
-- 选择更严格的「失败也计」。
CREATE TABLE IF NOT EXISTS `bp_interface_quota` (
  `interface_id` bigint NOT NULL,
  `enabled` tinyint NOT NULL DEFAULT 0 COMMENT '1 = enforce interface quota',
  `count_mode` varchar(16) NOT NULL DEFAULT 'attempt' COMMENT 'attempt/success',
  `per_user_hour` bigint NOT NULL DEFAULT 0 COMMENT 'Per-user cap per hour; 0 = unlimited',
  `per_user_day` bigint NOT NULL DEFAULT 0 COMMENT 'Per-user cap per day; 0 = unlimited',
  `per_user_week` bigint NOT NULL DEFAULT 0 COMMENT 'Per-user cap per ISO week; 0 = unlimited',
  `per_user_month` bigint NOT NULL DEFAULT 0 COMMENT 'Per-user cap per calendar month; 0 = unlimited',
  `all_users_hour` bigint NOT NULL DEFAULT 0 COMMENT 'Interface-wide cap per hour; 0 = unlimited',
  `all_users_day` bigint NOT NULL DEFAULT 0 COMMENT 'Interface-wide cap per day; 0 = unlimited',
  `all_users_week` bigint NOT NULL DEFAULT 0 COMMENT 'Interface-wide cap per ISO week; 0 = unlimited',
  `all_users_month` bigint NOT NULL DEFAULT 0 COMMENT 'Interface-wide cap per calendar month; 0 = unlimited',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`interface_id`),
  CONSTRAINT `chk_bp_interface_quota_count_mode`
    CHECK (`count_mode` IN ('attempt', 'success')),
  CONSTRAINT `chk_bp_interface_quota_non_negative`
    CHECK (`per_user_hour` >= 0 AND `per_user_day` >= 0 AND `per_user_week` >= 0 AND `per_user_month` >= 0
           AND `all_users_hour` >= 0 AND `all_users_day` >= 0 AND `all_users_week` >= 0 AND `all_users_month` >= 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='Per-interface user and platform request quotas';

-- 用户组对「单接口单用户上限」的覆盖：
--   inherit   = 使用接口默认单用户上限；
--   unlimited = 该组不受单用户上限限制（接口全平台总量仍然生效）；
--   adjust    = 在接口默认值上加减（delta 可为负；默认值 0=不限时保持不限）；
--   override  = 直接使用本行的绝对值（0=不限）。
-- 这是「按用户组灵活配置」的唯一入口；全平台总量不支持按用户组改。
CREATE TABLE IF NOT EXISTS `bp_interface_role_quota` (
  `interface_id` bigint NOT NULL,
  `role_id` bigint NOT NULL,
  `mode` varchar(16) NOT NULL DEFAULT 'inherit' COMMENT 'inherit/unlimited/adjust/override',
  `adjust_hour` bigint NOT NULL DEFAULT 0 COMMENT 'Signed delta applied to per_user_hour',
  `adjust_day` bigint NOT NULL DEFAULT 0 COMMENT 'Signed delta applied to per_user_day',
  `adjust_week` bigint NOT NULL DEFAULT 0 COMMENT 'Signed delta applied to per_user_week',
  `adjust_month` bigint NOT NULL DEFAULT 0 COMMENT 'Signed delta applied to per_user_month',
  `override_hour` bigint NOT NULL DEFAULT 0 COMMENT 'Absolute per-user cap per hour; 0 = unlimited',
  `override_day` bigint NOT NULL DEFAULT 0 COMMENT 'Absolute per-user cap per day; 0 = unlimited',
  `override_week` bigint NOT NULL DEFAULT 0 COMMENT 'Absolute per-user cap per ISO week; 0 = unlimited',
  `override_month` bigint NOT NULL DEFAULT 0 COMMENT 'Absolute per-user cap per calendar month; 0 = unlimited',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`interface_id`, `role_id`),
  KEY `idx_bp_interface_role_quota_role` (`role_id`),
  CONSTRAINT `chk_bp_interface_role_quota_mode`
    CHECK (`mode` IN ('inherit', 'unlimited', 'adjust', 'override')),
  CONSTRAINT `chk_bp_interface_role_quota_override_non_negative`
    CHECK (`override_hour` >= 0 AND `override_day` >= 0 AND `override_week` >= 0 AND `override_month` >= 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='Per-role overrides for per-interface per-user quotas';


-- ============================================================
-- Migration: 000121_notice_hub.up.sql
-- ============================================================

-- 公告三合一：「公告管理」「首页滚动公告」「弹窗公告」原来分属两套数据、两个菜单、
-- 两个 API 模块。首页滚动公告是一张独立的单例配置表（bp_home_notice_config），
-- 只能放 300 字纯文本，既不参与公告中心列表，也不能置顶、不能设失效时间。
--
-- 这里把首页滚动位降级成公告条目自身的一个投放位（is_home_ticker），让所有对外
-- 文案共用同一条生命周期：一份正文、一套权限、一个生效/失效窗口。
--
-- bp_home_notice_config 保留结构与数据（本迁移不改动它），回滚到 1.2.70 及更早
-- 版本后旧代码仍可原样读取。

ALTER TABLE `bp_notice`
  ADD COLUMN `is_home_ticker` tinyint NOT NULL DEFAULT 0
    COMMENT '是否投放到首页滚动公告条' AFTER `is_popup`,
  ADD COLUMN `expire_time` datetime DEFAULT NULL
    COMMENT '失效时间，NULL=不自动失效' AFTER `publish_time`,
  ADD KEY `idx_bp_notice_ticker` (`status`, `is_home_ticker`, `publish_time`, `id`);

-- 标题同时充当首页滚动文案，所以放宽到原配置表的 300 字，
-- 否则下面迁入的长文案会被静默截断。
ALTER TABLE `bp_notice`
  MODIFY COLUMN `title` varchar(300) NOT NULL COMMENT '公告标题；首页滚动位取标题原文';

-- 把当前生效的首页滚动文案迁成一条公告。creator_id 留空作为「本迁移生成的行」的
-- 标记，down 迁移与后续清理都据此判断，不会误删管理员自己建的滚动公告。
INSERT INTO `bp_notice`
  (`title`, `summary`, `content_html`, `is_top`, `is_popup`, `is_home_ticker`,
   `status`, `publish_time`, `creator_id`, `updater_id`)
SELECT
  TRIM(o.`content`),
  LEFT(REPLACE(REPLACE(REPLACE(TRIM(o.`content`), '&', '&amp;'), '<', '&lt;'), '>', '&gt;'), 255),
  CONCAT('<p>', REPLACE(REPLACE(REPLACE(TRIM(o.`content`), '&', '&amp;'), '<', '&lt;'), '>', '&gt;'), '</p>'),
  1, 0, 1, 1, o.`updated_at`, NULL, NULL
FROM `bp_home_notice_config` o
WHERE o.`id` = 1
  AND o.`enabled` = 1
  AND TRIM(o.`content`) <> ''
  AND NOT EXISTS (SELECT 1 FROM `bp_notice` t WHERE t.`is_home_ticker` = 1);

-- 滚动文案的译文跟着走：home_notice.content -> 新公告的 title。
-- 关联键用「迁移行的标记 + 发布时间等于配置更新时间 + 标题等于原文」三重锁定，
-- 避开 golang-migrate 逐句执行时 LAST_INSERT_ID() 可能落到不同连接的问题。
INSERT INTO `bp_i18n_entry`
  (`resource_type`, `resource_key`, `field`, `locale`, `value`, `source_hash`, `updated_by`)
SELECT 'notice', CAST(n.`id` AS CHAR), 'title', e.`locale`, e.`value`, e.`source_hash`, e.`updated_by`
FROM `bp_i18n_entry` e
JOIN `bp_home_notice_config` o ON o.`id` = 1
JOIN `bp_notice` n
  ON n.`is_home_ticker` = 1
 AND n.`creator_id` IS NULL
 AND n.`publish_time` = o.`updated_at`
 AND n.`title` = TRIM(o.`content`)
WHERE e.`resource_type` = 'home_notice'
  AND e.`resource_key` = '1'
  AND e.`field` = 'content'
  AND TRIM(o.`content`) <> ''
ON DUPLICATE KEY UPDATE
  `value` = VALUES(`value`),
  `source_hash` = VALUES(`source_hash`),
  `updated_by` = VALUES(`updated_by`);

DELETE FROM `bp_i18n_entry` WHERE `resource_type` = 'home_notice';

-- 121 升为唯一可见入口（公告中心），128 保留成同组件的隐藏别名：
-- 不删行、不改 auth_code，已被单独授权 notice:home-config 的角色照旧能进滚动位页签，
-- 也不会因为多授予 notice:list 而看到以前看不到的公告列表。
UPDATE `bp_menu`
SET `component` = '/notice/admin/hub',
    `meta` = JSON_SET(
      COALESCE(`meta`, JSON_OBJECT()),
      '$.title', 'system.notice.hubTitle',
      '$.icon', 'mdi:bullhorn-outline',
      '$.order', 1
    ),
    `status` = 1
WHERE `id` = 121;

UPDATE `bp_menu`
SET `component` = '/notice/admin/hub',
    `meta` = JSON_SET(
      COALESCE(`meta`, JSON_OBJECT()),
      '$.hideInMenu', true,
      '$.navigationCleanup', '000121-notice-hub'
    ),
    `status` = 1
WHERE `id` = 128;


-- ============================================================
-- Migration: 000122_admin_navigation_groups.up.sql
-- ============================================================

-- 侧边栏分组重排：顶级菜单原本按「谁先建谁在前」堆积（order 混用 -1/3/4/5/9995/9996/9997
-- 与 2147483647），支付藏在「套餐管理」里、菜单管理藏在「系统管理」里、站内信群发也埋在
-- 系统管理下，同级还有 60/84 同为 order 3、230/176 同为 order 7 这类并列值，排序只能靠
-- id 兜底。这里按业务动线重排成 11 组，并把同级 order 摊成 10/20/30 留插队余量。
--
-- 只动 pid 与 meta.order/title/component：菜单权限码（auth_code）与 bp_role_menu 授权关系
-- 一律不改，已被单独授权子页的角色会由 UserMenus 的祖先回溯自动看到新父级，无需重新授权。
--
-- 另修两处真实错位：
--   1. 账单记录（83）的 component 直接写成接口日志页 /interface/log，与「调用日志」共用
--      同一条组件指针；改成 /package/billing-records 后仍由前端别名表解析到同一页面，
--      而该页依据路由路径（…/billing-records）判定「仅计费流水」模式，行为不变、来源可辨。
--   2. 16 行 meta.title 是历史错误字符集导入留下的字面「?」（迁移源文件本身是 UTF-8），
--      侧边栏与菜单管理页都会显示成问号。这里改成前端已有的 i18n 键，中英文各一份。

-- ── 1. 顶级分组：顺序 + 标题 ────────────────────────────────────────────────
UPDATE `bp_menu`
SET `meta` = JSON_SET(
  COALESCE(`meta`, JSON_OBJECT()),
  '$.title', 'navigation.admin.interfaceGateway',
  '$.order', 20
)
WHERE `id` = 40 AND `name` = 'InterfaceManagement';

UPDATE `bp_menu`
SET `meta` = JSON_SET(
  COALESCE(`meta`, JSON_OBJECT()),
  '$.order', 30,
  '$.adminOrder', 30,
  '$.userOrder', 3
)
WHERE `id` = 60 AND `name` = 'ApiKeyManagement';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.order', 40)
WHERE `id` = 70 AND `name` = 'PackageManagement';

-- 支付原本是套餐管理的子目录，续费/对账与套餐配置是两条独立动线，提升为顶级。
UPDATE `bp_menu`
SET `pid` = 0,
    `meta` = JSON_SET(
      COALESCE(`meta`, JSON_OBJECT()),
      '$.title', 'navigation.admin.payment',
      '$.order', 50
    )
WHERE `id` = 100 AND `name` = 'PaymentManagement';

UPDATE `bp_menu`
SET `meta` = JSON_SET(
  COALESCE(`meta`, JSON_OBJECT()),
  '$.title', 'navigation.admin.userPermissions',
  '$.order', 60
)
WHERE `id` = 10 AND `name` = 'UserManagement';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.order', 70)
WHERE `id` = 120 AND `name` = 'ContentOperations';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.order', 80)
WHERE `id` = 90 AND `name` = 'DashboardStats';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.order', 100)
WHERE `id` = 30 AND `name` = 'System';

-- ── 2. 新增「工单与支持」分组，收拢工单管理与站内信群发 ────────────────────
-- 204 是页面（type=menu），不能直接当容器；新建目录承接，原路径与组件保持不变。
INSERT INTO `bp_menu`
  (`id`, `pid`, `name`, `path`, `component`, `redirect`, `type`, `auth_code`, `meta`, `status`)
SELECT
  900100, 0, 'TicketSupportHub', '/admin/support', NULL, '/admin/tickets', 'catalog', NULL,
  JSON_OBJECT('title', 'navigation.admin.ticketsSupport', 'icon', 'lucide:life-buoy', 'order', 90, 'audience', 'admin'),
  1
FROM DUAL
WHERE NOT EXISTS (SELECT 1 FROM `bp_menu` m WHERE m.`name` = 'TicketSupportHub');

UPDATE `bp_menu`
SET `pid` = 900100,
    `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.order', 10)
WHERE `id` = 204 AND `name` = 'AdminTicketManagement';

UPDATE `bp_menu`
SET `pid` = 900100,
    `meta` = JSON_SET(
      COALESCE(`meta`, JSON_OBJECT()),
      '$.order', 20,
      '$.audience', 'admin'
    )
WHERE `id` = 241 AND `name` = 'SystemInboxBroadcast';

-- ── 3. 子页归位 ────────────────────────────────────────────────────────────
-- 菜单管理属于「谁能看到什么」，与用户/角色同组，不再是系统管理的内部配置。
UPDATE `bp_menu`
SET `pid` = 10,
    `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.order', 30)
WHERE `id` = 31 AND `name` = 'SystemMenu';

-- 兑换码原本是独立顶级目录，实际与套餐/账单同属一条售卖动线。
-- 221-223 直接挂到套餐与账单下，220 保留成隐藏别名路由，旧书签仍可跳转。
UPDATE `bp_menu`
SET `pid` = 70,
    `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.order', 60)
WHERE `id` = 221 AND `name` = 'RedeemTypes';

UPDATE `bp_menu`
SET `pid` = 70,
    `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.order', 70)
WHERE `id` = 222 AND `name` = 'RedeemCodes';

UPDATE `bp_menu`
SET `pid` = 70,
    `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.order', 80)
WHERE `id` = 223 AND `name` = 'RedeemRecords';

UPDATE `bp_menu`
SET `redirect` = '/redeem/codes',
    `meta` = JSON_SET(
      COALESCE(`meta`, JSON_OBJECT()),
      '$.hideInMenu', true,
      '$.navigationCleanup', '000122-redeem-into-packages'
    )
WHERE `id` = 220 AND `name` = 'RedeemCode';

-- 操作日志与日志清理是数据侧运维，归「数据与风控」，不留在系统设置里。
UPDATE `bp_menu`
SET `pid` = 90,
    `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.order', 20)
WHERE `id` = 211 AND `name` = 'TicketAdminAttachment';

UPDATE `bp_menu`
SET `pid` = 90,
    `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.order', 30)
WHERE `id` = 160 AND `name` = 'SystemLogDelete';

-- ── 4. 同级 order 摊开为 10/20/30，消除并列 ─────────────────────────────────
UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.order', 10)
WHERE `id` = 41 AND `name` = 'InterfaceList';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.order', 20)
WHERE `id` = 50 AND `name` = 'InterfaceCallLog';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.order', 10)
WHERE `id` = 71 AND `name` = 'GlobalPackageList';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.order', 20)
WHERE `id` = 75 AND `name` = 'InterfacePackageList';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.order', 30)
WHERE `id` = 79 AND `name` = 'PointPackageList';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.order', 40)
WHERE `id` = 243 AND `name` = 'UserPackageList';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.order', 50)
WHERE `id` = 83 AND `name` = 'BillingRecordList';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.order', 10)
WHERE `id` = 101 AND `name` = 'PaymentConfig';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.order', 20)
WHERE `id` = 103 AND `name` = 'RechargePresetList';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.order', 30)
WHERE `id` = 107 AND `name` = 'PaymentOrderList';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.order', 10)
WHERE `id` = 11 AND `name` = 'SystemUser';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.order', 20)
WHERE `id` = 20 AND `name` = 'SystemRole';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.order', 10)
WHERE `id` = 121 AND `name` = 'NoticeList';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.order', 20)
WHERE `id` = 130 AND `name` = 'SiteConfig';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.order', 30)
WHERE `id` = 138 AND `name` = 'FriendLinkCenter';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.order', 40)
WHERE `id` = 181 AND `name` = 'AdvertisementList';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.order', 50)
WHERE `id` = 190 AND `name` = 'DonationConfig';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.order', 10)
WHERE `id` = 170 AND `name` = 'RiskControlDashboard';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.order', 10)
WHERE `id` = 150 AND `name` = 'SystemRegisterOpenConfig';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.order', 20)
WHERE `id` = 152 AND `name` = 'SystemRegisterEmailConfig';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.order', 30)
WHERE `id` = 155 AND `name` = 'SystemRegisterMobileConfig';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.order', 40)
WHERE `id` = 158 AND `name` = 'SystemOAuthLoginConfig';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.order', 50)
WHERE `id` = 176 AND `name` = 'SystemIpSourceConfig';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.order', 60)
WHERE `id` = 179 AND `name` = 'SystemCaptchaConfig';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.order', 70)
WHERE `id` = 185 AND `name` = 'SystemCustomCode';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.order', 80)
WHERE `id` = 230 AND `name` = 'SystemOpenApiConfig';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.order', 90)
WHERE `id` = 232 AND `name` = 'SystemOpenApiKeys';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.order', 100)
WHERE `id` = 250 AND `name` = 'SystemTranslation';

-- 用户侧顶级顺序同时去并列：套餐与资产、服务支持、个人设置依次排在注入的
-- 接口服务(2)/调用日志(2.1)/我的密钥(3)/我的友链(4) 之后。
UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.order', 5)
WHERE `id` = 84 AND `name` = 'PackageMall';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.order', 6)
WHERE `id` = 210 AND `name` = 'UserServiceSupport';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.order', 7)
WHERE `id` = 203 AND `name` = 'Profile';

-- ── 5. 账单记录组件指针 ─────────────────────────────────────────────────────
UPDATE `bp_menu`
SET `component` = '/package/billing-records'
WHERE `id` = 83 AND `name` = 'BillingRecordList';

-- ── 6. 问号标题改回 i18n 键（沿用前端各功能页已有文案）─────────────────────
UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.title', 'ipConfig.title')
WHERE `id` = 176 AND `name` = 'SystemIpSourceConfig';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.title', 'ipConfig.save')
WHERE `id` = 177 AND `name` = 'SystemIpSourceConfigUpdate';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.title', 'captchaConfig.title')
WHERE `id` = 179 AND `name` = 'SystemCaptchaConfig';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.title', 'captchaConfig.save')
WHERE `id` = 180 AND `name` = 'SystemCaptchaConfigUpdate';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.title', 'advertisementAdmin.title')
WHERE `id` = 181 AND `name` = 'AdvertisementList';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.title', 'advertisementAdmin.create')
WHERE `id` = 182 AND `name` = 'AdvertisementCreate';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.title', 'advertisementAdmin.editor.edit')
WHERE `id` = 183 AND `name` = 'AdvertisementUpdate';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.title', 'advertisementAdmin.delete.action')
WHERE `id` = 184 AND `name` = 'AdvertisementDelete';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.title', 'customCode.title')
WHERE `id` = 185 AND `name` = 'SystemCustomCode';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.title', 'customCode.save')
WHERE `id` = 186 AND `name` = 'SystemCustomCodeUpdate';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.title', 'deliveryPolicy.title')
WHERE `id` = 187 AND `name` = 'InterfaceDeliveryPolicy';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.title', 'requestValidation.title')
WHERE `id` = 188 AND `name` = 'InterfaceRequestValidation';

UPDATE `bp_menu`
SET `meta` = JSON_SET(
  COALESCE(`meta`, JSON_OBJECT()),
  '$.title', 'siteSettings.loading.fields.upload'
)
WHERE `id` = 189 AND `name` = 'SiteLoadingAnimationUpload';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.title', 'donationSettings.title')
WHERE `id` = 190 AND `name` = 'DonationConfig';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.title', 'donationSettings.actions.save')
WHERE `id` = 191 AND `name` = 'DonationConfigUpdate';

UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.title', 'donationSettings.actions.uploadQrCode')
WHERE `id` = 192 AND `name` = 'DonationQRCodeUpload';


-- ============================================================
-- Migration: 000123_user_package_primary.up.sql
-- ============================================================

-- 用户套餐「主套餐」标记。
--
-- 语义：同一用户全局最多一条 is_primary=1 的权益（两表合计唯一）。
-- 网关扣减顺序 = is_primary DESC（主套餐先扣）→ end_at ASC（快到期的先扣）
--               → daily_limit DESC / qps_limit DESC（同批到期时高配的先扣）→ id ASC。
-- 主套餐耗尽（日限用完 / 到期）后自动回落到默认顺序，不需要用户干预。
--
-- 生产库曾在 1.2.70 期间手工补过这两列与索引（当时 schema_migrations 仍停在 120），
-- 所以本迁移按「列/索引已存在就跳过」的幂等写法执行：直接 ADD COLUMN 会在
-- 已手工补过的库上 1060 duplicate column 报错，把整个升级流程卡在 dirty 状态。
SET @bugpk_schema = DATABASE();

SET @bugpk_add_global_primary = IF(EXISTS(
  SELECT 1 FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_schema
    AND TABLE_NAME = 'bp_user_package_global'
    AND COLUMN_NAME = 'is_primary'
), 0, 1);
SET @bugpk_sql = IF(@bugpk_add_global_primary = 1,
  'ALTER TABLE `bp_user_package_global` ADD COLUMN `is_primary` tinyint NOT NULL DEFAULT 0 COMMENT ''主套餐：1=用户指定的优先扣减套餐'' AFTER `status`',
  'SELECT 1 WHERE 0');
PREPARE bugpk_stmt FROM @bugpk_sql;
EXECUTE bugpk_stmt;
DEALLOCATE PREPARE bugpk_stmt;

SET @bugpk_add_global_primary_idx = IF(EXISTS(
  SELECT 1 FROM information_schema.STATISTICS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_schema
    AND TABLE_NAME = 'bp_user_package_global'
    AND INDEX_NAME = 'idx_bp_user_package_global_primary'
), 0, 1);
SET @bugpk_sql = IF(@bugpk_add_global_primary_idx = 1,
  'ALTER TABLE `bp_user_package_global` ADD KEY `idx_bp_user_package_global_primary` (`user_id`, `is_primary`, `status`, `end_at`)',
  'SELECT 1 WHERE 0');
PREPARE bugpk_stmt FROM @bugpk_sql;
EXECUTE bugpk_stmt;
DEALLOCATE PREPARE bugpk_stmt;

SET @bugpk_add_interface_primary = IF(EXISTS(
  SELECT 1 FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_schema
    AND TABLE_NAME = 'bp_user_package_interface'
    AND COLUMN_NAME = 'is_primary'
), 0, 1);
SET @bugpk_sql = IF(@bugpk_add_interface_primary = 1,
  'ALTER TABLE `bp_user_package_interface` ADD COLUMN `is_primary` tinyint NOT NULL DEFAULT 0 COMMENT ''主套餐：1=用户指定的优先扣减套餐'' AFTER `status`',
  'SELECT 1 WHERE 0');
PREPARE bugpk_stmt FROM @bugpk_sql;
EXECUTE bugpk_stmt;
DEALLOCATE PREPARE bugpk_stmt;

SET @bugpk_add_interface_primary_idx = IF(EXISTS(
  SELECT 1 FROM information_schema.STATISTICS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_schema
    AND TABLE_NAME = 'bp_user_package_interface'
    AND INDEX_NAME = 'idx_bp_user_package_interface_primary'
), 0, 1);
SET @bugpk_sql = IF(@bugpk_add_interface_primary_idx = 1,
  'ALTER TABLE `bp_user_package_interface` ADD KEY `idx_bp_user_package_interface_primary` (`user_id`, `is_primary`, `status`, `end_at`)',
  'SELECT 1 WHERE 0');
PREPARE bugpk_stmt FROM @bugpk_sql;
EXECUTE bugpk_stmt;
DEALLOCATE PREPARE bugpk_stmt;


-- ============================================================
-- Migration: 000124_package_role_restriction.up.sql
-- ============================================================

-- 套餐按用户组售卖（2026-11-04）。
--
-- 平台上的「用户组」即 bp_role，用户通过 bp_user_role / primary_role_id 归属。
-- 三类套餐（全站 / 接口 / 点数）都支持限制购买用户组：
--   role_restricted  = 0 时完全保持原有行为；
--   role_restricted  = 1 时只有 bp_package_role 中的用户组可以购买；
--   role_visible_only = 1 时非允许用户组在套餐商城看不到该套餐（否则仅购买受限）。
-- 未配置任何允许用户组时视为不可购买，管理员保存时由服务层拒绝。
--
-- 三张表的加列都按「列已存在就跳过」执行：MySQL DDL 不是事务性的，
-- 本迁移在三张表中间任意一处失败都会留下「部分表已加列、版本停在 dirty」的状态，
-- 幂等写法让升级可原地重跑，不必人工核对哪张表改过。
SET @bugpk_schema = DATABASE();

SET @bugpk_add_global_role = IF(EXISTS(
  SELECT 1 FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_schema
    AND TABLE_NAME = 'bp_package_global'
    AND COLUMN_NAME = 'role_restricted'
), 0, 1);
SET @bugpk_sql = IF(@bugpk_add_global_role = 1,
  'ALTER TABLE `bp_package_global` ADD COLUMN `role_restricted` tinyint NOT NULL DEFAULT 0 COMMENT ''是否限制用户组购买：0不限制，1限制'' AFTER `sale_end_at`, ADD COLUMN `role_visible_only` tinyint NOT NULL DEFAULT 0 COMMENT ''限制时是否仅对允许用户组可见：0所有人可见，1仅允许组可见'' AFTER `role_restricted`',
  'SELECT 1 WHERE 0');
PREPARE bugpk_stmt FROM @bugpk_sql;
EXECUTE bugpk_stmt;
DEALLOCATE PREPARE bugpk_stmt;

SET @bugpk_add_point_role = IF(EXISTS(
  SELECT 1 FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_schema
    AND TABLE_NAME = 'bp_package_point'
    AND COLUMN_NAME = 'role_restricted'
), 0, 1);
SET @bugpk_sql = IF(@bugpk_add_point_role = 1,
  'ALTER TABLE `bp_package_point` ADD COLUMN `role_restricted` tinyint NOT NULL DEFAULT 0 COMMENT ''是否限制用户组购买：0不限制，1限制'' AFTER `sale_end_at`, ADD COLUMN `role_visible_only` tinyint NOT NULL DEFAULT 0 COMMENT ''限制时是否仅对允许用户组可见：0所有人可见，1仅允许组可见'' AFTER `role_restricted`',
  'SELECT 1 WHERE 0');
PREPARE bugpk_stmt FROM @bugpk_sql;
EXECUTE bugpk_stmt;
DEALLOCATE PREPARE bugpk_stmt;

SET @bugpk_add_interface_role = IF(EXISTS(
  SELECT 1 FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_schema
    AND TABLE_NAME = 'bp_package_interface'
    AND COLUMN_NAME = 'role_restricted'
), 0, 1);
SET @bugpk_sql = IF(@bugpk_add_interface_role = 1,
  'ALTER TABLE `bp_package_interface` ADD COLUMN `role_restricted` tinyint NOT NULL DEFAULT 0 COMMENT ''是否限制用户组购买：0不限制，1限制'' AFTER `remark`, ADD COLUMN `role_visible_only` tinyint NOT NULL DEFAULT 0 COMMENT ''限制时是否仅对允许用户组可见：0所有人可见，1仅允许组可见'' AFTER `role_restricted`',
  'SELECT 1 WHERE 0');
PREPARE bugpk_stmt FROM @bugpk_sql;
EXECUTE bugpk_stmt;
DEALLOCATE PREPARE bugpk_stmt;

CREATE TABLE IF NOT EXISTS `bp_package_role` (
  `package_type` varchar(16) NOT NULL COMMENT '套餐类型：GLOBAL/INTERFACE/POINT',
  `package_id` bigint NOT NULL COMMENT '套餐ID；接口套餐为 bp_package_interface.id',
  `role_id` bigint NOT NULL COMMENT '用户组（角色）ID',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  PRIMARY KEY (`package_type`, `package_id`, `role_id`),
  KEY `idx_bp_package_role_role` (`role_id`),
  CONSTRAINT `chk_bp_package_role_type` CHECK (`package_type` IN ('GLOBAL', 'INTERFACE', 'POINT'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='套餐允许的用户组';


-- ============================================================
-- Migration: 000125_package_total_limit.up.sql
-- ============================================================

-- 套餐总调用量 + 计量方式（2026-11-04）。
--
-- total_limit：套餐生命周期内的总调用量上限，0 = 不限。
--   模板表（bp_package_global / bp_package_interface_spec）配置，
--   发放到 bp_user_package_global / bp_user_package_interface 时快照；
--   网关扣减与日限同一事务，任一上限打满即不再消耗该权益。
--   与 daily_limit 同时配置时要求 total_limit >= daily_limit（服务层校验），
--   否则日限永远先于总量触顶，总量配置成为死配置。
--
-- count_mode：该套餐的计量口径，发行时快照到权益行：
--   success = 仅成功计量（默认，保持既有行为：预留 → 失败释放时回退计数）；
--   attempt = 失败也计量（失败释放时不回退 day_used/total_used，
--             计费记录仍置 RELEASED，仅计数不退）。
--
-- 四张表的加列都按「列已存在就跳过」执行：MySQL DDL 不是事务性的，
-- 幂等写法让升级可原地重跑。
SET @bugpk_schema = DATABASE();

SET @bugpk_add_pkg_global = IF(EXISTS(
  SELECT 1 FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_schema
    AND TABLE_NAME = 'bp_package_global'
    AND COLUMN_NAME = 'total_limit'
), 0, 1);
SET @bugpk_sql = IF(@bugpk_add_pkg_global = 1,
  'ALTER TABLE `bp_package_global` ADD COLUMN `total_limit` bigint NOT NULL DEFAULT 0 COMMENT ''总调用量上限，0不限'' AFTER `daily_limit`, ADD COLUMN `count_mode` varchar(16) NOT NULL DEFAULT ''success'' COMMENT ''计量方式：success=仅成功计量，attempt=失败也计量'' AFTER `total_limit`',
  'SELECT 1 WHERE 0');
PREPARE bugpk_stmt FROM @bugpk_sql;
EXECUTE bugpk_stmt;
DEALLOCATE PREPARE bugpk_stmt;

SET @bugpk_add_pkg_spec = IF(EXISTS(
  SELECT 1 FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_schema
    AND TABLE_NAME = 'bp_package_interface_spec'
    AND COLUMN_NAME = 'total_limit'
), 0, 1);
SET @bugpk_sql = IF(@bugpk_add_pkg_spec = 1,
  'ALTER TABLE `bp_package_interface_spec` ADD COLUMN `total_limit` bigint NOT NULL DEFAULT 0 COMMENT ''总调用量上限，0不限'' AFTER `daily_limit`, ADD COLUMN `count_mode` varchar(16) NOT NULL DEFAULT ''success'' COMMENT ''计量方式：success=仅成功计量，attempt=失败也计量'' AFTER `total_limit`',
  'SELECT 1 WHERE 0');
PREPARE bugpk_stmt FROM @bugpk_sql;
EXECUTE bugpk_stmt;
DEALLOCATE PREPARE bugpk_stmt;

SET @bugpk_add_user_global = IF(EXISTS(
  SELECT 1 FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_schema
    AND TABLE_NAME = 'bp_user_package_global'
    AND COLUMN_NAME = 'total_limit'
), 0, 1);
SET @bugpk_sql = IF(@bugpk_add_user_global = 1,
  'ALTER TABLE `bp_user_package_global` ADD COLUMN `total_limit` bigint NOT NULL DEFAULT 0 COMMENT ''总调用量上限快照，0不限'' AFTER `daily_limit`, ADD COLUMN `count_mode` varchar(16) NOT NULL DEFAULT ''success'' COMMENT ''计量方式快照：success=仅成功计量，attempt=失败也计量'' AFTER `total_limit`',
  'SELECT 1 WHERE 0');
PREPARE bugpk_stmt FROM @bugpk_sql;
EXECUTE bugpk_stmt;
DEALLOCATE PREPARE bugpk_stmt;

SET @bugpk_add_user_interface = IF(EXISTS(
  SELECT 1 FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_schema
    AND TABLE_NAME = 'bp_user_package_interface'
    AND COLUMN_NAME = 'total_limit'
), 0, 1);
SET @bugpk_sql = IF(@bugpk_add_user_interface = 1,
  'ALTER TABLE `bp_user_package_interface` ADD COLUMN `total_limit` bigint NOT NULL DEFAULT 0 COMMENT ''总调用量上限快照，0不限'' AFTER `daily_limit`, ADD COLUMN `count_mode` varchar(16) NOT NULL DEFAULT ''success'' COMMENT ''计量方式快照：success=仅成功计量，attempt=失败也计量'' AFTER `total_limit`',
  'SELECT 1 WHERE 0');
PREPARE bugpk_stmt FROM @bugpk_sql;
EXECUTE bugpk_stmt;
DEALLOCATE PREPARE bugpk_stmt;


-- ============================================================
-- Migration: 000126_package_hidden_and_grant.up.sql
-- ============================================================

-- 套餐隐藏开关 + 管理端下发套餐（2026-11-04）。
--
-- hidden = 1 的套餐在商城（/package/mall/*）对所有人不可见也不可购买，
-- 管理端列表照常可见，可通过「下发套餐」接口直接把权益发给指定用户。
-- 隐藏只影响售卖面，不影响已发放权益的扣减与展示。
--
-- 加列按「列已存在就跳过」执行：MySQL DDL 不是事务性的，
-- 幂等写法让升级可原地重跑（与 000124 同款模板）。
SET @bugpk_schema = DATABASE();

SET @bugpk_add_global_hidden = IF(EXISTS(
  SELECT 1 FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_schema
    AND TABLE_NAME = 'bp_package_global'
    AND COLUMN_NAME = 'hidden'
), 0, 1);
SET @bugpk_sql = IF(@bugpk_add_global_hidden = 1,
  'ALTER TABLE `bp_package_global` ADD COLUMN `hidden` tinyint NOT NULL DEFAULT 0 COMMENT ''商城隐藏：0正常售卖，1商城隐藏且不可购买，仅管理端可下发'' AFTER `role_visible_only`',
  'SELECT 1 WHERE 0');
PREPARE bugpk_stmt FROM @bugpk_sql;
EXECUTE bugpk_stmt;
DEALLOCATE PREPARE bugpk_stmt;

SET @bugpk_add_point_hidden = IF(EXISTS(
  SELECT 1 FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_schema
    AND TABLE_NAME = 'bp_package_point'
    AND COLUMN_NAME = 'hidden'
), 0, 1);
SET @bugpk_sql = IF(@bugpk_add_point_hidden = 1,
  'ALTER TABLE `bp_package_point` ADD COLUMN `hidden` tinyint NOT NULL DEFAULT 0 COMMENT ''商城隐藏：0正常售卖，1商城隐藏且不可购买，仅管理端可下发'' AFTER `role_visible_only`',
  'SELECT 1 WHERE 0');
PREPARE bugpk_stmt FROM @bugpk_sql;
EXECUTE bugpk_stmt;
DEALLOCATE PREPARE bugpk_stmt;

SET @bugpk_add_interface_hidden = IF(EXISTS(
  SELECT 1 FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_schema
    AND TABLE_NAME = 'bp_package_interface'
    AND COLUMN_NAME = 'hidden'
), 0, 1);
SET @bugpk_sql = IF(@bugpk_add_interface_hidden = 1,
  'ALTER TABLE `bp_package_interface` ADD COLUMN `hidden` tinyint NOT NULL DEFAULT 0 COMMENT ''商城隐藏：0正常售卖，1商城隐藏且不可购买，仅管理端可下发（对全部规格生效）'' AFTER `role_visible_only`',
  'SELECT 1 WHERE 0');
PREPARE bugpk_stmt FROM @bugpk_sql;
EXECUTE bugpk_stmt;
DEALLOCATE PREPARE bugpk_stmt;

-- 按钮权限：向用户下发套餐（挂在「用户套餐」菜单 243 下，246 空闲）
INSERT INTO `bp_menu`
  (`id`, `pid`, `name`, `path`, `component`, `redirect`, `type`, `auth_code`, `meta`, `status`)
VALUES
  (246, 243, 'UserPackageGrant', '', NULL, NULL, 'button', 'package:user:grant', JSON_OBJECT('title', 'system.package.userGrantAction'), 1)
ON DUPLICATE KEY UPDATE
  `pid` = VALUES(`pid`),
  `type` = VALUES(`type`),
  `auth_code` = VALUES(`auth_code`),
  `meta` = VALUES(`meta`),
  `status` = VALUES(`status`);

INSERT IGNORE INTO `bp_role_menu` (`role_id`, `menu_id`)
SELECT 1, `id` FROM `bp_menu` WHERE `id` = 246;


-- ============================================================
-- Migration: 000127_user_totp.up.sql
-- ============================================================

-- 用户 2FA（TOTP）与恢复码（2026-11-04）。
--
-- bp_user 三列：
--   totp_secret      加密后的 Base32 密钥（secretbox 加密，存储为文本）。
--                    加密密钥复用 systemConfigCipher（与 captcha secret_ref 同路），
--                    明文密钥永不落库。
--   totp_enabled     1 = 已通过「绑定 + 首次验证码确认」，登录/找回密码时强制校验。
--   totp_confirmed_at 确认启用时间，仅用于后台展示与审计。
--
-- bp_user_totp_recovery_code：2FA 启用时生成的一次性恢复码（10 个）。
--   code_hash 存 SHA-256（恢复码属于高熵随机值，无需 bcrypt）；
--   used_at 非空即已消费；登录成功路径原子 UPDATE 抢占，天然防并发重放。
--
-- 全部按「已存在就跳过」的幂等写法：MySQL DDL 不具备事务性，升级可原地重跑。
SET @bugpk_schema = DATABASE();

SET @bugpk_add_totp_secret = IF(EXISTS(
  SELECT 1 FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_schema
    AND TABLE_NAME = 'bp_user'
    AND COLUMN_NAME = 'totp_secret'
), 0, 1);
SET @bugpk_sql = IF(@bugpk_add_totp_secret = 1,
  'ALTER TABLE `bp_user` ADD COLUMN `totp_secret` varchar(255) NULL COMMENT ''TOTP 密钥（secretbox 加密存储）'' AFTER `token_version`',
  'SELECT 1 WHERE 0');
PREPARE bugpk_stmt FROM @bugpk_sql;
EXECUTE bugpk_stmt;
DEALLOCATE PREPARE bugpk_stmt;

SET @bugpk_add_totp_enabled = IF(EXISTS(
  SELECT 1 FROM information_schema.COLUMNS
  WHERE BINARY TABLE_SCHEMA = BINARY @bugpk_schema
    AND TABLE_NAME = 'bp_user'
    AND COLUMN_NAME = 'totp_enabled'
), 0, 1);
SET @bugpk_sql = IF(@bugpk_add_totp_enabled = 1,
  'ALTER TABLE `bp_user` ADD COLUMN `totp_enabled` tinyint NOT NULL DEFAULT 0 COMMENT ''2FA 是否启用'' AFTER `totp_secret`, ADD COLUMN `totp_confirmed_at` datetime NULL COMMENT ''2FA 启用确认时间'' AFTER `totp_enabled`',
  'SELECT 1 WHERE 0');
PREPARE bugpk_stmt FROM @bugpk_sql;
EXECUTE bugpk_stmt;
DEALLOCATE PREPARE bugpk_stmt;

CREATE TABLE IF NOT EXISTS `bp_user_totp_recovery_code` (
  `id` bigint NOT NULL AUTO_INCREMENT COMMENT '自增主键',
  `user_id` bigint NOT NULL COMMENT '用户 ID',
  `code_hash` char(64) NOT NULL COMMENT '恢复码 SHA-256 十六进制',
  `used_at` datetime NULL COMMENT '消费时间，NULL=未使用',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '生成时间',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_user_code_hash` (`user_id`, `code_hash`),
  KEY `idx_totp_recovery_user_used` (`user_id`, `used_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='用户 2FA 一次性恢复码';


-- ============================================================
-- Migration: 000128_auth_session_config.up.sql
-- ============================================================

-- 登录态有效期后台可配置：
--   bp_auth_session_config 单例行（CHECK id=1，模式与 bp_auth_register_config 一致），
--   存 access token 分钟数与 refresh token 天数；运行时由 systemsettings 域读写，
--   保存后通过观察者回调推给 auth.JWTService 覆盖 config.yaml/env 里的静态值。
-- 默认值与 config.AuthConfig 默认（30 分钟 / 7 天）保持一致，未配置时行为不变。
CREATE TABLE IF NOT EXISTS `bp_auth_session_config` (
  `id` BIGINT UNSIGNED NOT NULL,
  `access_token_minutes` INT UNSIGNED NOT NULL DEFAULT 30,
  `refresh_token_days` INT UNSIGNED NOT NULL DEFAULT 7,
  `updated_at` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  CONSTRAINT `chk_bp_auth_session_config_singleton` CHECK (`id` = 1)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_unicode_ci;

INSERT IGNORE INTO `bp_auth_session_config` (`id`) VALUES (1);

-- 后台「系统设置」补充「登录态配置」入口（前端路由 /system/session-config 静态注册）。
-- 菜单 ID 取 900110/900111：当前最大种子 ID 为 900100（工单分组），留 10 的间隔防撞。
INSERT INTO `bp_menu`
  (`id`, `pid`, `name`, `path`, `component`, `redirect`, `type`, `auth_code`, `meta`, `status`)
VALUES
  (900110, 30, 'SystemSessionConfig', '/system/session-config', '/system/session-config/index', NULL, 'menu', 'system:session-config:view',
   JSON_OBJECT('title', 'sessionConfig.title', 'icon', 'mdi:timer-outline', 'order', 45, 'audience', 'admin'), 1),
  (900111, 900110, 'SystemSessionConfigUpdate', '', NULL, NULL, 'button', 'system:session-config:update',
   JSON_OBJECT('title', 'sessionConfig.save', 'order', 1), 1)
ON DUPLICATE KEY UPDATE
  `pid` = VALUES(`pid`),
  `path` = VALUES(`path`),
  `component` = VALUES(`component`),
  `redirect` = VALUES(`redirect`),
  `type` = VALUES(`type`),
  `auth_code` = VALUES(`auth_code`),
  `meta` = VALUES(`meta`),
  `status` = VALUES(`status`);

-- 授权给管理员角色（role_id=1），与其它菜单种子保持一致。
INSERT IGNORE INTO `bp_role_menu` (`role_id`, `menu_id`)
SELECT 1, id FROM `bp_menu` WHERE id BETWEEN 900110 AND 900111;


-- ============================================================
-- Migration: 000129_payment_currency.up.sql
-- ============================================================

-- 000129: 支付货币配置（人民币/美元，后台可切换）。
-- 站点结算货币为单选：CNY（默认，行为与历史一致）/ USD。
-- USD 模式下订单 pay_amount 按 usd_cny_rate 从商品原价（人民币）换算，
-- 原价保留在 bp_payment_order.origin_amount 便于审计与对账。

CREATE TABLE IF NOT EXISTS `bp_payment_currency_config` (
  `id` bigint NOT NULL DEFAULT 1 COMMENT 'singleton id',
  `currency` varchar(8) NOT NULL DEFAULT 'CNY' COMMENT 'settlement currency: CNY/USD',
  `usd_cny_rate` decimal(12,4) NOT NULL DEFAULT 7.2000 COMMENT '1 USD = X CNY, applied when currency=USD',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='Payment currency config';

INSERT INTO `bp_payment_currency_config` (`id`, `currency`, `usd_cny_rate`)
VALUES (1, 'CNY', 7.2000)
ON DUPLICATE KEY UPDATE `id` = `id`;

ALTER TABLE `bp_payment_order`
  ADD COLUMN `currency` varchar(8) NOT NULL DEFAULT 'CNY' COMMENT 'settlement currency at order time' AFTER `pay_channel`,
  ADD COLUMN `origin_amount` decimal(12,4) DEFAULT NULL COMMENT 'original CNY price before currency conversion' AFTER `currency`;


-- ============================================================
-- Migration: 000130_payment_sponsor_role.up.sql
-- ============================================================

-- 充值赞助自动升级用户组（2026-11-04）。
-- RECHARGE 订单结算入账时，把付款用户的主用户组切换到本表配置的目标用户组。
-- enabled=0（默认）时保持原行为；role_id=0 视为未配置。
CREATE TABLE IF NOT EXISTS `bp_payment_sponsor_config` (
  `id` bigint NOT NULL COMMENT '单例行，恒为 1',
  `enabled` tinyint NOT NULL DEFAULT 0 COMMENT '充值后自动切换用户组：0关闭，1开启',
  `role_id` bigint NOT NULL DEFAULT 0 COMMENT '目标用户组编号，0=未配置',
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='充值赞助自动升级用户组配置';

INSERT INTO `bp_payment_sponsor_config` (`id`, `enabled`, `role_id`)
VALUES (1, 0, 0)
ON DUPLICATE KEY UPDATE `id` = VALUES(`id`);


-- ============================================================
-- Migration: 000131_payment_trc20.up.sql
-- ============================================================

-- 000131: TRC20/USDT 链上充值通道（海外站）。
-- 充值订单额外记录链上收款地址、精确金额（含对账分位）、txid 与确认数；
-- 转账流水表以 txid 唯一幂等，轮询与用户提交共用同一份匹配依据。

CREATE TABLE IF NOT EXISTS `bp_payment_trc20_config` (
  `id` bigint NOT NULL DEFAULT 1 COMMENT 'singleton id',
  `enabled` tinyint NOT NULL DEFAULT 0 COMMENT 'TRC20 充值开关',
  `network` varchar(16) NOT NULL DEFAULT 'TRON' COMMENT '链网络',
  `address` varchar(64) NOT NULL DEFAULT '' COMMENT 'USDT-TRC20 收款地址',
  `api_key_cipher` text COMMENT 'TronGrid API Key（加密存储，可空）',
  `contract_address` varchar(64) NOT NULL DEFAULT 'TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t' COMMENT 'USDT 合约地址',
  `min_amount` decimal(18,6) NOT NULL DEFAULT 5.000000 COMMENT '最小充值 USDT',
  `max_amount` decimal(18,6) NOT NULL DEFAULT 10000.000000 COMMENT '单笔最大 USDT',
  `confirmations` int NOT NULL DEFAULT 1 COMMENT '入账所需确认数',
  `cursor_timestamp_ms` bigint NOT NULL DEFAULT 0 COMMENT '轮询游标（区块时间毫秒）',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  CONSTRAINT `chk_bp_trc20_network` CHECK (`network` IN ('TRON'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='TRC20 充值通道配置';

INSERT INTO `bp_payment_trc20_config` (`id`, `enabled`, `network`, `address`)
VALUES (1, 0, 'TRON', '')
ON DUPLICATE KEY UPDATE `id` = VALUES(`id`);

CREATE TABLE IF NOT EXISTS `bp_payment_trc20_transfer` (
  `id` bigint NOT NULL AUTO_INCREMENT,
  `txid` varchar(128) NOT NULL COMMENT '链上交易号',
  `from_address` varchar(64) NOT NULL DEFAULT '',
  `to_address` varchar(64) NOT NULL DEFAULT '',
  `amount` decimal(18,6) NOT NULL DEFAULT 0 COMMENT 'USDT 金额',
  `block_timestamp` bigint NOT NULL DEFAULT 0 COMMENT '区块时间（毫秒）',
  `matched_order_no` varchar(64) DEFAULT NULL COMMENT '匹配到的充值订单号',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_bp_trc20_transfer_txid` (`txid`),
  KEY `idx_bp_trc20_transfer_matched` (`matched_order_no`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='TRC20 到账流水（txid 幂等）';

ALTER TABLE `bp_payment_order`
  ADD COLUMN `chain_address` varchar(64) DEFAULT NULL COMMENT 'TRC20 收款地址快照' AFTER `channel_trade_no`,
  ADD COLUMN `chain_amount` decimal(18,6) DEFAULT NULL COMMENT 'TRC20 精确到账金额（USDT）' AFTER `chain_address`,
  ADD COLUMN `chain_txid` varchar(128) DEFAULT NULL COMMENT 'TRC20 交易号' AFTER `chain_amount`,
  ADD COLUMN `chain_confirm_count` int NOT NULL DEFAULT 0 COMMENT 'TRC20 确认数' AFTER `chain_txid`,
  ADD UNIQUE KEY `uk_bp_payment_order_chain_txid` (`chain_txid`),
  ADD KEY `idx_bp_payment_order_chain_match` (`pay_channel`, `status`, `chain_amount`);

ALTER TABLE `bp_payment_method` DROP CHECK `chk_bp_payment_method_code`;
ALTER TABLE `bp_payment_method`
  ADD CONSTRAINT `chk_bp_payment_method_code` CHECK (`method_code` IN ('BALANCE','ALIPAY','WECHAT','HUPIJIAO_ALIPAY','HUPIJIAO_WECHAT','TRC20'));

INSERT INTO `bp_payment_method` (`method_code`, `enabled`, `sort_order`)
VALUES ('TRC20', 0, 60)
ON DUPLICATE KEY UPDATE `method_code` = VALUES(`method_code`);


-- ============================================================
-- Migration: 000132_payment_order_cancel_delete.up.sql
-- ============================================================

-- 000132: 交易记录支持「取消订单」与「删除订单」。
--
-- deleted_at：用户与管理员的删除都只是软隐藏。订单行保留，支付审计与旧支付载荷不丢，
-- 迟到的到账通知仍能补结算；只有列表查询过滤 deleted_at，按订单号查询与结算路径不过滤。
--
-- CANCELLED 此前只是只读枚举，没有写入方；现在由用户主动取消写入。它与 EXPIRED 同类，
-- 代表「本地不再允许发起支付」而不是渠道关单（渠道关单是 CLOSED），因此随后渠道确认
-- 到账时依然允许结算并把已释放的限量套餐占用补回。
ALTER TABLE `bp_payment_order`
  ADD COLUMN `deleted_at` datetime DEFAULT NULL COMMENT '交易记录删除（软隐藏）时间' AFTER `settled_at`;

-- 管理端「删除订单」按钮权限，挂在 107 支付订单列表下。
-- 菜单 ID 取 900112：当前最大种子 ID 为 900111（登录态配置按钮）。
INSERT INTO `bp_menu`
  (`id`, `pid`, `name`, `path`, `component`, `redirect`, `type`, `auth_code`, `meta`, `status`)
VALUES
  (900112, 107, 'PaymentOrderDelete', '', NULL, NULL, 'button', 'payment:order:delete',
   JSON_OBJECT('title', 'Delete Payment Order'), 1)
ON DUPLICATE KEY UPDATE
  `pid` = VALUES(`pid`),
  `type` = VALUES(`type`),
  `auth_code` = VALUES(`auth_code`),
  `meta` = VALUES(`meta`),
  `status` = VALUES(`status`);

INSERT IGNORE INTO `bp_role_menu` (`role_id`, `menu_id`)
SELECT 1, id FROM `bp_menu` WHERE id = 900112;


-- ============================================================
-- Migration: 000133_metering_policy.up.sql
-- ============================================================

-- 接口计量策略：把「扣不扣」拆成三个正交维度（2026-11-05）。
--
-- 现状是 bp_interface_api.billing_enabled 一个布尔试图同时管「资产扣减」和
-- 「次数计量」，而套餐次数它根本管不到。这里把三个维度拆开：
--
--   asset_mode   —— 余额 / 点数：inherit = 按 price、point_price 和
--                   billing_enabled 决定（即 1.2.88 现有行为）；
--                   free = 永不扣资产（保留价格配置，重开即恢复）；
--                   charge = 只要接口有价格就必扣，并跳过「扣费条件」判定；
--                            价格与点数全为 0 时无从扣起，仍按零价接口处理。
--   package_mode —— 套餐次数（bp_user_package_*.{day_used,total_used}）：
--                   inherit = 有可用套餐就扣（现有行为）；
--                   consume = 强制计入套餐；
--                   skip = 不占套餐次数。
--   group_mode   —— 用户组免费额度窗口（分/时/日/月）：
--                   inherit = 按 bp_role 的配额开关与窗口计数（现有行为）；
--                   count = 即使角色关闭了配额也计数；
--                   skip = 不计入免费额度窗口。
--
-- 三列默认全部 inherit，因此本迁移落库后线上行为零变化；需要破例的接口再逐条配置。
--
-- 兜底闸门不受本策略约束，代码里写死不可豁免：
--   * 注册后 24h 的首日额度（防开新号白嫖）；
--   * bp_interface_quota 的全平台总量 all_users_*。
-- 保存时若某条解析后的策略路径为「资产 free + 套餐 skip + 免费额度 skip」，
-- 必须已启用接口配额并设置至少一项全平台总量，否则拒绝保存。
CREATE TABLE IF NOT EXISTS `bp_interface_metering_policy` (
  `interface_id` bigint NOT NULL COMMENT '接口编号',
  `asset_mode` varchar(16) NOT NULL DEFAULT 'inherit' COMMENT '资产扣减：inherit/free/charge',
  `package_mode` varchar(16) NOT NULL DEFAULT 'inherit' COMMENT '套餐次数：inherit/consume/skip',
  `group_mode` varchar(16) NOT NULL DEFAULT 'inherit' COMMENT '用户组免费额度：inherit/count/skip',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (`interface_id`),
  CONSTRAINT `chk_bp_interface_metering_asset_mode`
    CHECK (`asset_mode` IN ('inherit', 'free', 'charge')),
  CONSTRAINT `chk_bp_interface_metering_package_mode`
    CHECK (`package_mode` IN ('inherit', 'consume', 'skip')),
  CONSTRAINT `chk_bp_interface_metering_group_mode`
    CHECK (`group_mode` IN ('inherit', 'count', 'skip'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='接口计量策略（资产/套餐/用户组免费额度三维）';

-- 用户组对三维策略的覆盖，与 bp_interface_role_quota 同一套解析思路：
--   inherit = 沿用本接口的策略值；其余取值 = 直接指定该维度的行为。
-- 只有存在非 inherit 维度时才落一行，缺行等价于三维全 inherit。
CREATE TABLE IF NOT EXISTS `bp_interface_role_metering` (
  `interface_id` bigint NOT NULL COMMENT '接口编号',
  `role_id` bigint NOT NULL COMMENT '用户组编号',
  `asset_mode` varchar(16) NOT NULL DEFAULT 'inherit' COMMENT '资产扣减：inherit/free/charge',
  `package_mode` varchar(16) NOT NULL DEFAULT 'inherit' COMMENT '套餐次数：inherit/consume/skip',
  `group_mode` varchar(16) NOT NULL DEFAULT 'inherit' COMMENT '用户组免费额度：inherit/count/skip',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (`interface_id`, `role_id`),
  KEY `idx_bp_interface_role_metering_role` (`role_id`),
  CONSTRAINT `chk_bp_interface_role_metering_asset_mode`
    CHECK (`asset_mode` IN ('inherit', 'free', 'charge')),
  CONSTRAINT `chk_bp_interface_role_metering_package_mode`
    CHECK (`package_mode` IN ('inherit', 'consume', 'skip')),
  CONSTRAINT `chk_bp_interface_role_metering_group_mode`
    CHECK (`group_mode` IN ('inherit', 'count', 'skip'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='按用户组覆盖接口计量策略';


-- ============================================================
-- Migration: 000134_payment_goods_name.up.sql
-- ============================================================

-- 站点级「支付商品名」：微信/支付宝收银台与虎皮椒 title 统一用的中性名称（2026-11-05）。
--
-- 现状是商品名直接取订单的 subject（「余额充值-50元」「接口套餐-X-Y」这类明细名），
-- 它同时是账单一、订单记录和到账通知里的名称。虎皮椒走微信通道时，「充值」「点数」
-- 「套餐」这类虚拟商品字样容易被风控判定，导致支付受限或掉单。
--
-- 这里把「发给渠道的商品名」与「订单明细名」拆开：
--   * goods_name 非空 → 下单请求里的 title/description/subject 一律用它；
--   * goods_name 为空 → 沿用订单明细名（即历史行为，零变化）。
-- bp_payment_order.subject 始终保留明细名，对账、通知与用户订单列表不受影响。
--
-- 挂在 bp_payment_currency_config 这张站点级单例表上：同一份代码部署国内站/国际站时，
-- 各自后台独立取值，改完即时生效，不需要发版。
-- 虎皮椒官方限制 title ≤ 127 字节，因此保存时按字节校验，超长直接拒绝。
ALTER TABLE `bp_payment_currency_config`
  ADD COLUMN `goods_name` varchar(128) NOT NULL DEFAULT '' COMMENT '发给支付渠道的商品名；留空则沿用订单明细名' AFTER `usd_cny_rate`;

-- 默认给国内版一个风控友好的中性名称；已有部署首次跑本迁移即生效，
-- 后台改成空串可退回按明细名展示。
UPDATE `bp_payment_currency_config`
SET `goods_name` = 'API 开放平台技术服务费'
WHERE `id` = 1 AND `goods_name` = '';


-- ============================================================
-- Migration: 000135_package_visible_role.up.sql
-- ============================================================

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


-- ============================================================
-- Migration: 000136_trc20_retry_cursor.up.sql
-- ============================================================

ALTER TABLE bp_payment_trc20_config
  ADD COLUMN retry_cursor_id bigint NOT NULL DEFAULT 0 COMMENT 'Durable round-robin receipt retry cursor';
ALTER TABLE bp_payment_order
  ADD KEY idx_bp_payment_order_chain_window (pay_channel, chain_amount, created_at);


-- ============================================================
-- Migration: 000137_interface_quota_cover_package.up.sql
-- ============================================================

-- 000137: 接口配额是否覆盖套餐用户（套餐豁免选项）。
--
-- 默认 0（豁免）：保留「套餐优先」——本次请求已由套餐额度承接时，
-- 整层接口配额不检查也不计数，套餐用户只受套餐日/总量额度约束。
-- 设为 1（不豁免）：接口配额对套餐用户同样生效（与用户组总量配额叠加）。
ALTER TABLE `bp_interface_quota`
  ADD COLUMN `cover_package` tinyint NOT NULL DEFAULT 0
  COMMENT '1 = 接口配额同样约束套餐用户；0 = 套餐优先，已由套餐承接的请求跳过本层' AFTER `count_mode`;


-- ============================================================
-- Migration: 000138_interface_degrade_policy.up.sql
-- ============================================================

-- 000138: 接口按用户组次数自动降级。
--
-- 源接口 A 配置一个默认目标接口 B，并为部分用户组设置阈值与计数窗口。
-- 用户在某个窗口内对 A 的已放行请求达到阈值后，后续请求仍调用 A 的网关路径，
-- 但上游执行配置切换为 B；鉴权、计费、配额、限流和调用日志仍按 A 执行。
CREATE TABLE IF NOT EXISTS `bp_interface_degrade_policy` (
  `interface_id` bigint NOT NULL,
  `enabled` tinyint NOT NULL DEFAULT 0 COMMENT '1 = enable degrade for this source interface',
  `default_target_interface_id` bigint DEFAULT NULL COMMENT 'Default target interface; NULL = not configured',
  `policy_version` bigint NOT NULL DEFAULT 1 COMMENT 'Bumped on material changes; resets counters',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`interface_id`),
  KEY `idx_bp_interface_degrade_target` (`default_target_interface_id`),
  CONSTRAINT `chk_bp_interface_degrade_enabled`
    CHECK (`enabled` IN (0, 1)),
  CONSTRAINT `chk_bp_interface_degrade_version`
    CHECK (`policy_version` >= 1)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='Per-interface degrade policy: default target interface';

CREATE TABLE IF NOT EXISTS `bp_interface_role_degrade` (
  `interface_id` bigint NOT NULL,
  `role_id` bigint NOT NULL,
  `count_window` varchar(16) NOT NULL DEFAULT 'day' COMMENT 'day/week/month/total',
  `threshold` bigint NOT NULL DEFAULT 0 COMMENT 'Requests before degrade; 0 = disabled for this role',
  `target_interface_id` bigint DEFAULT NULL COMMENT 'NULL = inherit the interface default target',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`interface_id`, `role_id`),
  KEY `idx_bp_interface_role_degrade_role` (`role_id`),
  KEY `idx_bp_interface_role_degrade_target` (`target_interface_id`),
  CONSTRAINT `chk_bp_interface_role_degrade_window`
    CHECK (`count_window` IN ('day', 'week', 'month', 'total')),
  CONSTRAINT `chk_bp_interface_role_degrade_threshold`
    CHECK (`threshold` >= 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='Per-role thresholds for interface degrade';


-- ============================================================
-- Migration: 000139_interface_degrade_custom_target.up.sql
-- ============================================================

-- 000139: 降级目标支持自定义内部地址。
--
-- 与接口轮询的上游节点一致，降级目标可以不是已注册的公开接口，而是
-- 管理员直接填写的 HTTP(S) 内部地址。默认目标与用户组覆盖都支持：
--   * 填写 target_interface_id => 使用已注册接口的执行配置；
--   * 填写 target_url          => 使用当前接口的清洗/代理/超时配置，仅替换上游地址；
--   * 两者只能二选一，留空表示跟随默认目标。
ALTER TABLE `bp_interface_degrade_policy`
  ADD COLUMN `default_target_url` varchar(1024) DEFAULT NULL
  COMMENT 'Custom internal target URL; mutually exclusive with default_target_interface_id'
  AFTER `default_target_interface_id`;

ALTER TABLE `bp_interface_role_degrade`
  ADD COLUMN `target_url` varchar(1024) DEFAULT NULL
  COMMENT 'Custom internal target URL; NULL = inherit default target'
  AFTER `target_interface_id`;


-- ============================================================
-- Migration: 000140_interface_quota_role_ignore.up.sql
-- ============================================================

-- 000140: 接口配额的用户组覆盖新增 ignore 模式。
--
-- 与既有 unlimited 的区别：
--   unlimited = 只放开「单用户上限」，请求仍会检查并消耗全平台总量计数；
--   ignore    = 整层跳过：不检查、不计数，连全平台总量都不占。
--
-- 典型用途：内部账号 / 赞助 / 白名单用户组的流量不占用对外售卖的全平台额度池。
-- 需要注意：ignore 的请求完全不受本层约束（QPS、用户组总量配额、首日额度仍然生效），
-- 因此上游实际承受的流量会高于 all_users_* 配置值，属于管理端显式选择的结果。
ALTER TABLE `bp_interface_role_quota` DROP CHECK `chk_bp_interface_role_quota_mode`;
ALTER TABLE `bp_interface_role_quota`
  ADD CONSTRAINT `chk_bp_interface_role_quota_mode`
  CHECK (`mode` IN ('inherit', 'unlimited', 'adjust', 'override', 'ignore'));


-- ============================================================
-- Migration: 000141_payment_binancepay.up.sql
-- ============================================================

-- 000141: 币安 Pay 商户充值通道（覆盖「链下转账」）。
--
-- 为什么需要独立通道：交易所站内转账不产生任何链上交易，链上扫描永远看不到；
-- 只有币安自己的推送/查单能证明这笔钱存在。因此它与 TRC20 并列，不能替代。
--
-- nonce 表用于回调幂等：币安会重复推送同一笔，重复必须被识别为已处理。

CREATE TABLE IF NOT EXISTS `bp_payment_binancepay_config` (
  `id` bigint NOT NULL DEFAULT 1 COMMENT 'singleton id',
  `enabled` tinyint NOT NULL DEFAULT 0 COMMENT '币安 Pay 通道开关',
  `merchant_id` varchar(64) NOT NULL DEFAULT '' COMMENT 'Binance Pay 商户号',
  `cert_serial` varchar(128) NOT NULL DEFAULT '' COMMENT '商户 API 证书序列号',
  `secret_key_cipher` text COMMENT 'HMAC-SHA512 密钥（加密存储）',
  `api_key_cipher` text COMMENT 'Binance API Key（加密存储，可空）',
  `pay_currency` varchar(16) NOT NULL DEFAULT 'USDT' COMMENT '收款币种',
  `min_amount` decimal(18,6) NOT NULL DEFAULT 5.000000 COMMENT '最小充值金额',
  `max_amount` decimal(18,6) NOT NULL DEFAULT 10000.000000 COMMENT '单笔最大金额',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='币安 Pay 商户配置';

INSERT INTO `bp_payment_binancepay_config` (`id`, `enabled`, `pay_currency`)
VALUES (1, 0, 'USDT')
ON DUPLICATE KEY UPDATE `id` = VALUES(`id`);

-- 回调幂等键：同一 nonce 只允许入账一次。
-- 主键即业务键，重复插入直接失败，天然防重放。
CREATE TABLE IF NOT EXISTS `bp_payment_binancepay_nonce` (
  `nonce_key` varchar(160) NOT NULL COMMENT '商户号:nonce',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (`nonce_key`),
  KEY `idx_bp_binancepay_nonce_created` (`created_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='币安 Pay 回调幂等记录';

-- 币安 Pay 不产生链上交易号，但结算事务里 txid 那列被用来存渠道流水号。
-- 订单表的 chain_txid 长度足够容纳币安 transactionId，无需变更。


-- ============================================================
-- Migration: 000142_package_renewal.up.sql
-- ============================================================

-- 000142: 套餐续费顺延。
--
-- 问题：原购买路径是纯 INSERT（insertGlobalEntitlementTx / BP 接口套餐同款），
-- 用户在有效期内再买一次同一套餐，库里会变成两条各自从 now() 起算的权益。
-- 网关按 end_at asc 先扣最早到期的那张，于是后买的那张在整个前周期内都在排队，
-- 等于白送一段有效期——用户付了两次钱，实际天数没有叠加。
--
-- 本迁移只加两样东西：
--   1. bp_package_renewal_log —— 每次顺延都留一条流水，回滚与退款都靠它算；
--   2. renewal_enabled 开关 —— 体验档/总限量套餐这类不适合顺延的，显式关掉。
-- 权益表本身不动：顺延是对已有行的 end_at 做加法，不改结构。

CREATE TABLE IF NOT EXISTS `bp_package_renewal_log` (
  `id` bigint NOT NULL AUTO_INCREMENT COMMENT '续费流水编号',
  `user_id` bigint NOT NULL COMMENT '用户编号',
  `scope` varchar(16) NOT NULL COMMENT 'GLOBAL / INTERFACE',
  `entitlement_id` bigint NOT NULL COMMENT '被顺延的 bp_user_package_* 记录编号',
  `package_id` bigint NOT NULL DEFAULT 0 COMMENT '套餐编号',
  `spec_id` bigint NOT NULL DEFAULT 0 COMMENT '接口套餐规格编号（全站套餐为 0）',
  `add_days` int NOT NULL COMMENT '本次顺延天数',
  `old_end_at` datetime NOT NULL COMMENT '顺延前到期时间',
  `new_end_at` datetime NOT NULL COMMENT '顺延后到期时间',
  `amount` decimal(12,4) NOT NULL DEFAULT 0.0000 COMMENT '本次续费金额',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  PRIMARY KEY (`id`),
  -- 回滚按钮按 entitlement 反查，退款按 user 反查，两条都要走索引
  KEY `idx_bp_package_renewal_ent` (`scope`, `entitlement_id`, `id`),
  KEY `idx_bp_package_renewal_user` (`user_id`, `created_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='套餐续费顺延流水';

-- 默认全开：绝大多数套餐希望续费是顺延而不是多给一张并行券。
-- 到期时间按「已到期的那天 + 天数」推，不给历史数据留空窗。
ALTER TABLE `bp_package_global`
  ADD COLUMN `renewal_enabled` tinyint NOT NULL DEFAULT 1 COMMENT '1=有效期内重复购买时顺延到期时间，0=每次购买新建一条权益';

ALTER TABLE `bp_package_interface_spec`
  ADD COLUMN `renewal_enabled` tinyint NOT NULL DEFAULT 1 COMMENT '1=有效期内重复购买时顺延到期时间，0=每次购买新建一条权益';


-- ============================================================
-- Migration: 000143_payment_bep20.up.sql
-- ============================================================

-- 000143: BEP20（BSC）USDT 链上充值通道。
--
-- 与 TRC20 并列的第二种 USDT 收款网络：同一个充值订单模型（精确到分位金额用于
-- 链上匹配），但使用 EVM JSON-RPC（eth_getLogs / eth_getTransactionReceipt）对账，
-- 不依赖第三方索引 API Key。收款地址、RPC 节点、金额范围与确认数均可后台配置。

CREATE TABLE IF NOT EXISTS `bp_payment_bep20_config` (
  `id` bigint NOT NULL DEFAULT 1 COMMENT 'singleton id',
  `enabled` tinyint NOT NULL DEFAULT 0 COMMENT 'BEP20 充值开关',
  `network` varchar(16) NOT NULL DEFAULT 'BSC' COMMENT '链网络',
  `address` varchar(64) NOT NULL DEFAULT '' COMMENT 'USDT-BEP20 收款地址（0x 开头）',
  `contract_address` varchar(64) NOT NULL DEFAULT '0x55d398326f99059ff775485246999027b3197955' COMMENT 'BSC USDT 合约地址',
  `rpc_url` varchar(255) NOT NULL DEFAULT 'https://bsc-rpc.publicnode.com' COMMENT 'BSC JSON-RPC 节点',
  `min_amount` decimal(18,6) NOT NULL DEFAULT 5.000000 COMMENT '最小充值 USDT',
  `max_amount` decimal(18,6) NOT NULL DEFAULT 10000.000000 COMMENT '单笔最大 USDT',
  `confirmations` int NOT NULL DEFAULT 15 COMMENT '入账所需区块确认数',
  `cursor_block` bigint NOT NULL DEFAULT 0 COMMENT '已完成的扫描区块高水位',
  `retry_cursor_id` bigint NOT NULL DEFAULT 0 COMMENT '逐笔重试游标',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  CONSTRAINT `chk_bp_bep20_network` CHECK (`network` IN ('BSC'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='BEP20 充值通道配置';

INSERT INTO `bp_payment_bep20_config` (`id`, `enabled`, `network`, `address`)
VALUES (1, 0, 'BSC', '')
ON DUPLICATE KEY UPDATE `id` = VALUES(`id`);

-- 一笔 EVM 交易可能产生多个 Transfer 日志（合约归集、批量转账），
-- 因此幂等键是 (tx_hash, log_index)，而不是只看交易号。
CREATE TABLE IF NOT EXISTS `bp_payment_bep20_transfer` (
  `id` bigint NOT NULL AUTO_INCREMENT,
  `tx_hash` varchar(80) NOT NULL COMMENT '链上交易哈希（0x + 64 hex）',
  `log_index` int NOT NULL DEFAULT 0 COMMENT '交易内日志序号',
  `from_address` varchar(64) NOT NULL DEFAULT '',
  `to_address` varchar(64) NOT NULL DEFAULT '',
  `amount` decimal(18,6) NOT NULL DEFAULT 0 COMMENT 'USDT 金额',
  `block_number` bigint NOT NULL DEFAULT 0 COMMENT '区块号',
  `block_timestamp` bigint NOT NULL DEFAULT 0 COMMENT '区块时间（毫秒）',
  `matched_order_no` varchar(64) DEFAULT NULL COMMENT '匹配到的充值订单号',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_bp_bep20_transfer_log` (`tx_hash`, `log_index`),
  KEY `idx_bp_bep20_transfer_matched` (`matched_order_no`),
  KEY `idx_bp_bep20_transfer_to_block` (`to_address`, `block_number`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='BEP20 到账流水（tx+log 幂等）';

ALTER TABLE `bp_payment_method` DROP CHECK `chk_bp_payment_method_code`;
ALTER TABLE `bp_payment_method`
  ADD CONSTRAINT `chk_bp_payment_method_code` CHECK (`method_code` IN ('BALANCE','ALIPAY','WECHAT','HUPIJIAO_ALIPAY','HUPIJIAO_WECHAT','TRC20','BEP20'));

INSERT INTO `bp_payment_method` (`method_code`, `enabled`, `sort_order`)
VALUES ('BEP20', 0, 65)
ON DUPLICATE KEY UPDATE `method_code` = VALUES(`method_code`);


-- ============================================================
-- Migration: 000144_agreement_templates.up.sql
-- ============================================================

-- 000143: 协议模板（公益运营 / 付费商业化运营两套）。
--
-- 背景：同一份程序会部署成国内公益站与海外付费站，支付协议、隐私政策、
-- 服务协议的口径并不相同。此前法务页正文写死在前端 i18n，运营改一句话
-- 都要发版；两套口径也无法在同一后台切换。
--
-- 设计：
--   * bp_agreement_template —— 每个 (文档, 模式, 语言) 一行。正文存净化的 HTML，
--     保存时按公告富文本同一策略过滤；status=0 表示停用，前台回退到内置文案。
--   * bp_agreement_config  —— 单例，active_mode 决定前台展示哪一套模板。
--   * 语言维度是为了不破坏英文站：没有对应英文模板时 configured=false，
--     前台继续使用现有内置英文文案，不会把中文模板硬塞给英文访客。
--
-- 首次安装默认生效公益运营模式，并内置六个中文模板（三种文档 × 两种模式）。

CREATE TABLE IF NOT EXISTS `bp_agreement_template` (
  `id` bigint NOT NULL AUTO_INCREMENT,
  `doc_key` varchar(32) NOT NULL COMMENT '文档类型：payment/privacy/terms',
  `mode` varchar(16) NOT NULL COMMENT '运营模式：welfare=公益，commercial=付费商业化',
  `lang` varchar(16) NOT NULL COMMENT '语言：zh-CN/en-US',
  `title` varchar(200) NOT NULL DEFAULT '' COMMENT '协议标题',
  `summary` varchar(500) NOT NULL DEFAULT '' COMMENT '协议摘要，用于法务页首屏',
  `version` varchar(32) NOT NULL DEFAULT 'v1.0' COMMENT '版本号',
  `effective_date` date DEFAULT NULL COMMENT '生效日期',
  `content_html` mediumtext NOT NULL COMMENT '协议正文 HTML（保存时净化）',
  `status` tinyint NOT NULL DEFAULT 1 COMMENT '1=启用，0=停用（前台回退内置文案）',
  `updated_by` bigint NOT NULL DEFAULT 0 COMMENT '最后修改人',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_bp_agreement_template` (`doc_key`, `mode`, `lang`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='协议模板（公益/商业化双模式）';

CREATE TABLE IF NOT EXISTS `bp_agreement_config` (
  `id` bigint unsigned NOT NULL,
  `active_mode` varchar(16) NOT NULL DEFAULT 'welfare' COMMENT '前台生效的运营模式：welfare/commercial',
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  CONSTRAINT `chk_bp_agreement_config_singleton` CHECK (`id` = 1),
  CONSTRAINT `chk_bp_agreement_config_mode` CHECK (`active_mode` IN ('welfare', 'commercial'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='协议模板生效模式（单例）';

INSERT IGNORE INTO `bp_agreement_config` (`id`, `active_mode`) VALUES (1, 'welfare');

-- ── 内置模板：公益运营模式 ─────────────────────────────────────────────────

INSERT INTO `bp_agreement_template`
  (`doc_key`, `mode`, `lang`, `title`, `summary`, `version`, `effective_date`, `content_html`, `status`)
VALUES
('payment', 'welfare', 'zh-CN',
 '公益运营模式支付与赞助协议',
 '本协议约定你向本站提供赞助的自愿性质、回馈额度的使用规则、退款与费用扣除方式，以及双方的责任边界。提交赞助前请完整阅读全部条款。',
 'v1.0', '2026-11-16',
 '<h2>一、协议主体与适用范围</h2>
<p>本协议由你（以下称“赞助人”）与本站运营方（以下称“本站”）就公益运营模式下的赞助支持与回馈额度使用事宜订立，适用于本站全部赞助入口。</p>
<p>本协议与《隐私政策》《用户服务协议》共同构成完整约定；就赞助事项表述不一致时，以本协议为准。</p>
<h2>二、赞助性质</h2>
<p><strong>你在本站完成的一切支付均为自愿赞助，不构成买卖、服务采购、消费合同或任何等价交换关系。</strong>本站不以盈利为目的，赞助款项用于覆盖服务器、带宽、接口维护等基础运营成本。</p>
<ul>
<li>赞助完全出于自愿，本站不承诺任何形式的等值回报。</li>
<li>赞助不构成投资、入股、借款、预付卡或任何具有货币属性的凭证。</li>
<li>你不得以未获得预期回报为由要求返还赞助款。</li>
</ul>
<h2>三、回馈额度</h2>
<p>为感谢赞助，本站可向你的账号授予接口调用次数、点数或套餐有效期等虚拟权益。回馈额度由本站结合运营情况自主确定，不可转让、兑现、继承或分割，不具有财产属性。</p>
<h2>四、支付与到账</h2>
<p>支付通过微信支付、支付宝等具备合法资质的第三方渠道完成。到账时间以渠道通知为准；如遇延迟，请保留支付凭证并通过工单联系本站核对。</p>
<h2>五、退款与费用扣除</h2>
<p>赞助款一经支付即用于公益运营，原则上不予退款。确因重复支付、系统错误等本站原因需要退回的，本站将在核实后按支付渠道规则原路退回，并扣除渠道已产生的手续费。</p>
<h2>六、不予退款情形</h2>
<ul>
<li>已实际使用的回馈额度或已开通的套餐权益。</li>
<li>因你违反本协议或用户服务协议导致服务被限制、终止。</li>
<li>你自身原因（误操作、不再使用等）产生的退款请求。</li>
<li>法律法规或支付渠道规则明确规定不予退款的情形。</li>
</ul>
<h2>七、使用规范与禁止转售</h2>
<p>回馈额度仅限你本人或你所在组织内部使用。未经本站书面同意，不得转售、出租、出借、二次分发或以任何方式对外提供接口能力。</p>
<h2>八、服务变更与终止</h2>
<p>本站有权根据运营需要调整、暂停或终止全部或部分公益服务。调整前本站将尽合理努力通过公告等方式告知；因不可抗力或第三方原因导致的服务中断，本站不承担赔偿责任。</p>
<h2>九、责任限制</h2>
<p>本站按现状提供服务，不对接口的绝对可用性、准确性或适用性作出保证。因使用或无法使用服务产生的间接损失，本站不承担赔偿责任；法律另有强制规定的除外。</p>
<h2>十、协议更新与争议解决</h2>
<p>本站可不时更新本协议，更新后在本页面公布，自公布之日起生效。你继续使用服务即视为接受更新后的协议。争议双方应友好协商；协商不成的，提交本站运营方所在地有管辖权的人民法院处理。</p>
<h2>确认条款</h2>
<p><strong>勾选即表示：你已完整阅读并理解上述全部条款，特别是赞助性质、退款规则与责任限制；你自愿提供赞助，不要求等价交换，不主张投资回报。</strong></p>',
 1),

('privacy', 'welfare', 'zh-CN',
 '公益运营模式隐私政策',
 '本政策说明本站如何在公益运营模式下收集、使用、存储与保护你的个人信息，以及你享有的权利。',
 'v1.0', '2026-11-16',
 '<h2>一、适用范围</h2>
<p>本政策适用于本站以公益运营模式提供的全部页面、接口与管理功能。你使用本站服务即表示同意本政策。</p>
<h2>二、我们收集的信息</h2>
<ul>
<li>账号信息：用户名、邮箱、手机号（如你选择绑定）、密码哈希。</li>
<li>使用信息：接口调用记录、请求时间、来源 IP、错误信息与配额消耗。</li>
<li>设备与日志：浏览器类型、操作系统、访问时间与必要的安全日志。</li>
</ul>
<h2>三、信息使用目的</h2>
<p>我们仅为提供、维护与改进公益服务而使用上述信息，包括身份验证、配额计量、故障排查、安全防护与必要的运营统计。</p>
<h2>四、Cookie 与同类技术</h2>
<p>本站使用必要的 Cookie 维持登录态与安全校验。你可以通过浏览器设置清除或拒绝 Cookie，但可能导致部分功能不可用。</p>
<h2>五、信息共享与披露</h2>
<p>除以下情形外，我们不会向第三方共享你的个人信息：获得你的明确同意；为完成支付而向支付渠道提供必要信息；法律法规或监管机关要求。</p>
<h2>六、信息存储与保护</h2>
<p>信息存储于本站服务器，保存期限为实现本政策目的所必需的期限。我们采取访问控制、加密传输等合理措施保护信息安全，但无法保证绝对安全。</p>
<h2>七、你的权利</h2>
<p>你有权查询、更正或删除你的个人信息，也可以注销账号。行使权利可通过工单或本页面公布的联系方式提出，我们将在合理期限内处理。</p>
<h2>八、未成年人保护</h2>
<p>本站服务面向成年人。若你未满法定年龄，应在监护人同意与指导下使用本站服务。</p>
<h2>九、政策更新</h2>
<p>本政策可能随运营情况更新，更新后在本页面公布。重大变更将通过公告提示；你继续使用服务即视为接受更新后的政策。</p>
<h2>十、联系我们</h2>
<p>如对本政策有疑问，请通过本站页面公布的联系邮箱或工单与我们联系。</p>',
 1),

('terms', 'welfare', 'zh-CN',
 '公益运营模式用户服务协议',
 '本协议约定公益运营模式下本站服务的性质、账号使用规范、禁止行为与双方责任边界。',
 'v1.0', '2026-11-16',
 '<h2>一、服务说明</h2>
<p>本站以公益方式提供接口解析与相关工具服务，服务本身不收取固定费用。为覆盖基础运营成本，本站接受用户自愿赞助并按规则授予回馈额度。</p>
<h2>二、账号注册与安全</h2>
<p>你应提供真实、有效的注册信息，并妥善保管账号与密码。因你保管不善导致的账号被盗、信息泄露或财产损失，由你自行承担。</p>
<h2>三、使用规范</h2>
<ul>
<li>仅将本站服务用于合法用途，遵守上游平台规则与当地法律法规。</li>
<li>不得利用本站服务批量注册、恶意刷量、攻击系统或干扰他人正常使用。</li>
<li>不得转售、出租、二次分发接口能力，或绕过配额与计费机制。</li>
</ul>
<h2>四、禁止行为</h2>
<p>本站禁止任何违法、侵权、危害网络安全或违反公序良俗的行为。一经发现，本站有权立即限制或终止相关账号的使用。</p>
<h2>五、内容与知识产权</h2>
<p>本站的代码、页面设计、文案与接口文档受法律保护。未经许可，不得复制、修改、传播或用于商业目的。你通过本站获取的第三方内容，其权利归原权利人所有。</p>
<h2>六、赞助与回馈额度</h2>
<p>赞助与回馈额度的性质、使用与退款规则以《公益运营模式支付与赞助协议》为准。</p>
<h2>七、服务变更与终止</h2>
<p>本站有权根据运营需要调整、暂停或终止全部或部分服务。因不可抗力、上游平台调整或第三方原因导致的中断，本站不承担赔偿责任。</p>
<h2>八、免责声明</h2>
<p>本站按现状提供服务，不对解析结果的准确性、完整性或适用性作出保证。你应自行判断并承担使用服务产生的风险。</p>
<h2>九、违约责任</h2>
<p>如你违反本协议，本站有权采取限制调用、暂停服务、冻结或终止账号等措施；造成本站损失的，你应依法承担赔偿责任。</p>
<h2>十、协议更新</h2>
<p>本站可不时更新本协议并在本页面公布，更新内容自公布之日起生效。你继续使用服务即视为接受更新后的协议。</p>
<h2>十一、联系我们</h2>
<p>如对本协议有疑问，请通过本站页面公布的联系邮箱或工单与我们联系。</p>',
 1),

-- ── 内置模板：付费商业化运营模式 ───────────────────────────────────────────

('payment', 'commercial', 'zh-CN',
 '付费商业化运营支付协议',
 '本协议约定付费商业化运营模式下服务购买、权益发放、续费、退款与责任边界。支付前请完整阅读全部条款。',
 'v1.0', '2026-11-16',
 '<h2>一、协议主体与适用范围</h2>
<p>本协议由你与本站运营方就付费商业化运营模式下的服务购买与使用事宜订立，适用于本站全部充值、套餐购买、点数购买与额度追加等付费入口。</p>
<p>本协议与《隐私政策》《用户服务协议》共同构成完整约定；就付费事项表述不一致时，以本协议为准。</p>
<h2>二、服务内容与价格</h2>
<p>本站提供的付费服务范围、套餐档位、额度与价格以购买页面实时展示为准。本站有权根据运营情况调整价格与服务内容，调整不影响已支付订单。</p>
<h2>三、订单与支付</h2>
<p>你提交订单并完成支付后，订单即成立。支付通过具备合法资质的第三方渠道完成；因渠道、网络或银行原因导致的延迟，以渠道最终通知为准。请勿向任何人泄露支付凭证或验证码。</p>
<h2>四、权益发放与有效期</h2>
<p>付费权益在支付成功并完成结算后发放至你的账号。权益的有效期、调用额度与使用范围以套餐说明为准；到期未使用的部分不结转、不折现，法律另有规定的除外。</p>
<h2>五、续费与顺延</h2>
<p>同一套餐在有效期内重复购买时，如该套餐支持续费顺延，新的有效期将在原到期时间基础上顺延；不支持顺延的套餐以购买页面说明为准。</p>
<h2>六、退款规则</h2>
<p>退款申请按购买页面公示的退款规则处理。可退款订单在扣除已使用权益对应的费用及支付渠道手续费后原路退回；已使用完毕、已过期或明确标注不可退款的订单不支持退款。</p>
<h2>七、服务可用性</h2>
<p>本站将尽合理努力保障服务稳定，但不承诺服务绝对不中断。计划内维护将提前通过公告告知；因不可抗力、上游平台调整或第三方原因导致的中断，本站不承担由此产生的间接损失。</p>
<h2>八、用户义务</h2>
<ul>
<li>如实提供账号与支付信息，不得冒用他人身份或支付工具。</li>
<li>不得利用本站服务从事违法违规活动或侵害第三方权益。</li>
<li>不得转售、出租、出借接口能力或绕过本站配额与计费机制。</li>
</ul>
<h2>九、违规处理</h2>
<p>如你违反本协议或相关规则，本站有权视情节采取限制调用、暂停服务、冻结或终止账号等措施；已支付费用是否退还需结合违规情形与退款规则确定。</p>
<h2>十、责任限制</h2>
<p>在法律允许的范围内，本站对因使用或无法使用服务产生的间接损失、利润损失或数据损失不承担赔偿责任。本站对单笔订单承担的赔偿责任以该订单实际支付金额为上限。</p>
<h2>十一、协议变更与争议解决</h2>
<p>本站可不时更新本协议并在本页面公布，更新内容自公布之日起生效。你继续使用服务即视为接受更新后的协议。争议双方应友好协商；协商不成的，提交本站运营方所在地有管辖权的法院处理。</p>
<h2>确认条款</h2>
<p><strong>勾选即表示：你已完整阅读并同意本协议全部条款，理解付费服务的性质、退款规则与责任限制。</strong></p>',
 1),

('privacy', 'commercial', 'zh-CN',
 '付费商业化运营隐私政策',
 '本政策说明本站如何在付费商业化运营模式下收集、使用、存储与保护你的个人信息，包括支付与订单相关信息。',
 'v1.0', '2026-11-16',
 '<h2>一、适用范围</h2>
<p>本政策适用于本站以付费商业化运营模式提供的全部页面、接口与管理功能。你使用本站服务即表示同意本政策。</p>
<h2>二、我们收集的信息</h2>
<ul>
<li>账号信息：用户名、邮箱、手机号（如你选择绑定）、密码哈希。</li>
<li>订单与支付信息：订单号、商品或套餐名称、金额、支付渠道与交易状态。我们不存储你的银行卡号或支付密码。</li>
<li>使用信息：接口调用记录、请求时间、来源 IP、错误信息与配额消耗。</li>
<li>设备与日志：浏览器类型、操作系统、访问时间与必要的安全日志。</li>
</ul>
<h2>三、信息使用目的</h2>
<p>我们仅为提供、维护与改进付费服务而使用上述信息，包括身份验证、订单履约、配额计量、退款处理、故障排查、安全防护与合规审计。</p>
<h2>四、Cookie 与同类技术</h2>
<p>本站使用必要的 Cookie 维持登录态与安全校验。你可以通过浏览器设置清除或拒绝 Cookie，但可能导致部分功能不可用。</p>
<h2>五、信息共享与披露</h2>
<p>除以下情形外，我们不会向第三方共享你的个人信息：获得你的明确同意；为完成支付、退款而向支付渠道提供必要信息；法律法规或监管机关要求。</p>
<h2>六、信息存储与保护</h2>
<p>信息存储于本站服务器，保存期限为实现本政策目的、履行合同与法定义务所必需的期限，订单与交易记录可能依法留存更长时间。我们采取访问控制、加密传输等合理措施保护信息安全，但无法保证绝对安全。</p>
<h2>七、你的权利</h2>
<p>你有权查询、更正或删除你的个人信息，也可以注销账号。行使权利可通过工单或本页面公布的联系方式提出，我们将在合理期限内处理；依法必须留存的交易记录除外。</p>
<h2>八、跨境传输</h2>
<p>如你的访问涉及跨境数据传输，我们将遵守适用法律要求，并采取必要的保护措施。</p>
<h2>九、未成年人保护</h2>
<p>本站服务面向成年人。若你未满法定年龄，应在监护人同意与指导下使用本站服务。</p>
<h2>十、政策更新与联系我们</h2>
<p>本政策可能随运营情况更新，更新后在本页面公布。如对本政策有疑问，请通过本站页面公布的联系邮箱或工单与我们联系。</p>',
 1),

('terms', 'commercial', 'zh-CN',
 '付费商业化运营用户服务协议',
 '本协议约定付费商业化运营模式下服务购买、账号使用、禁止行为与双方责任边界。',
 'v1.0', '2026-11-16',
 '<h2>一、服务说明</h2>
<p>本站提供接口解析、额度套餐与相关工具服务，部分功能按套餐收费。付费服务的范围、价格与规则以购买页面展示为准。</p>
<h2>二、账号注册与安全</h2>
<p>你应提供真实、有效的注册信息，并妥善保管账号与密码。因你保管不善导致的账号被盗、信息泄露或财产损失，由你自行承担。</p>
<h2>三、订单与付费服务</h2>
<p>订单成立、权益发放、续费与退款规则以《付费商业化运营支付协议》为准。你应在支付前确认套餐内容、有效期与适用限制。</p>
<h2>四、使用规范</h2>
<ul>
<li>仅将本站服务用于合法用途，遵守上游平台规则与当地法律法规。</li>
<li>不得利用本站服务批量注册、恶意刷量、攻击系统或干扰他人正常使用。</li>
<li>不得转售、出租、二次分发接口能力，或绕过配额与计费机制。</li>
</ul>
<h2>五、禁止行为</h2>
<p>本站禁止任何违法、侵权、危害网络安全或违反公序良俗的行为。一经发现，本站有权立即限制或终止相关账号的使用。</p>
<h2>六、内容与知识产权</h2>
<p>本站的代码、页面设计、文案与接口文档受法律保护。未经许可，不得复制、修改、传播或用于商业目的。你通过本站获取的第三方内容，其权利归原权利人所有。</p>
<h2>七、服务变更与中止</h2>
<p>本站有权根据运营需要调整、暂停或终止全部或部分服务。计划内调整将提前通过公告告知；因不可抗力、上游平台调整或第三方原因导致的中断，本站不承担赔偿责任。</p>
<h2>八、免责声明</h2>
<p>本站按现状提供服务，不对解析结果的准确性、完整性或适用性作出保证。你应自行判断并承担使用服务产生的风险。</p>
<h2>九、违约责任</h2>
<p>如你违反本协议，本站有权采取限制调用、暂停服务、冻结或终止账号等措施；造成本站损失的，你应依法承担赔偿责任。</p>
<h2>十、协议更新</h2>
<p>本站可不时更新本协议并在本页面公布，更新内容自公布之日起生效。你继续使用服务即视为接受更新后的协议。</p>
<h2>十一、联系我们</h2>
<p>如对本协议有疑问，请通过本站页面公布的联系邮箱或工单与我们联系。</p>',
 1);

-- 后台「系统设置 → 协议模板」入口（前端路由 /system/agreement 静态注册）。
-- 菜单 ID 取 900113/900114：当前最大种子 ID 为 900112（支付订单删除按钮）。
INSERT INTO `bp_menu`
  (`id`, `pid`, `name`, `path`, `component`, `redirect`, `type`, `auth_code`, `meta`, `status`)
VALUES
  (900113, 30, 'SystemAgreementConfig', '/system/agreement', '/system/agreement/index', NULL, 'menu', 'system:agreement:view',
   JSON_OBJECT('title', 'agreementConfig.title', 'icon', 'mdi:file-sign', 'order', 46, 'audience', 'admin'), 1),
  (900114, 900113, 'SystemAgreementConfigUpdate', '', NULL, NULL, 'button', 'system:agreement:update',
   JSON_OBJECT('title', 'agreementConfig.save', 'order', 1), 1)
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
SELECT 1, id FROM `bp_menu` WHERE id BETWEEN 900113 AND 900114;


-- ============================================================
-- Migration: 000145_ticket_image_host.up.sql
-- ============================================================

-- 工单图片消息 + 可配置第三方图床（CloudFlare-ImgBed）。
-- 1) 附件表区分本地存储与图床外链，消息可绑定自己上传的图片；
-- 2) 图床配置单例行（授权码由 SYSTEM_CONFIG_SECRET 加密后存储）；
-- 3) 后台「系统设置」补充图床配置入口与按钮权限。

ALTER TABLE `bp_ticket_attachment`
  ADD COLUMN `storage_provider` varchar(32) NOT NULL DEFAULT 'local' COMMENT 'local or external image host provider' AFTER `size_bytes`,
  ADD COLUMN `external_url` varchar(1024) DEFAULT NULL COMMENT 'Public URL when stored on an external image host' AFTER `storage_provider`;

CREATE TABLE IF NOT EXISTS `bp_image_host_config` (
  `id` tinyint unsigned NOT NULL,
  `enabled` tinyint NOT NULL DEFAULT 0,
  `provider` varchar(32) NOT NULL DEFAULT 'cloudflare-imgbed',
  `base_url` varchar(512) NOT NULL DEFAULT '',
  `upload_path` varchar(128) NOT NULL DEFAULT '/upload',
  `auth_code` varchar(512) NOT NULL DEFAULT '',
  `upload_channel` varchar(32) NOT NULL DEFAULT '',
  `upload_folder` varchar(128) NOT NULL DEFAULT '',
  `max_bytes` bigint NOT NULL DEFAULT 10485760,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  CONSTRAINT `chk_bp_image_host_config_singleton` CHECK (`id` = 1)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='Third-party image host configuration';

INSERT IGNORE INTO `bp_image_host_config` (`id`) VALUES (1);

INSERT INTO `bp_menu`
  (`id`, `pid`, `name`, `path`, `component`, `redirect`, `type`, `auth_code`, `meta`, `status`)
VALUES
  (900120, 30, 'SystemImageHostConfig', '/system/image-host-config', '/system/image-host-config/index', NULL, 'menu', 'system:image-host:view',
   JSON_OBJECT('title', 'imageHostConfig.title', 'icon', 'mdi:cloud-upload-outline', 'order', 46, 'audience', 'admin'), 1),
  (900121, 900120, 'SystemImageHostConfigUpdate', '', NULL, NULL, 'button', 'system:image-host:update',
   JSON_OBJECT('title', 'imageHostConfig.save', 'order', 1), 1),
  (900122, 900120, 'SystemImageHostConfigTest', '', NULL, NULL, 'button', 'system:image-host:test',
   JSON_OBJECT('title', 'imageHostConfig.test', 'order', 2), 1)
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
SELECT 1, id FROM `bp_menu` WHERE id BETWEEN 900120 AND 900122;


-- ============================================================
-- Migration: 000146_user_asset_adjustment.up.sql
-- ============================================================

-- 000146: 用户资产增量调整（余额/点数增减）与资产变动记录。
--
-- 调整接口对 bp_user 行加锁后写入 bp_wallet_transaction(biz_type=ADMIN_BALANCE_ADJUST)，
-- 与充值结算的 balance=balance+? 串行化，避免管理员改余额时覆盖并发充值。
--
-- 管理端「资产变动记录」按 biz_type + created_at 分页读取收入/购买/调整类流水，
-- 新索引让该列表无需扫描按次调用预扣产生的大量 OPEN_CALL_* 行。
ALTER TABLE `bp_wallet_transaction`
  ADD KEY `idx_bp_wallet_transaction_biz_created` (`biz_type`, `created_at`);

-- 「余额调整」按钮权限，挂在 11 系统用户列表下。
-- 菜单 ID 取 900123：当前最大种子 ID 为 900122（图床配置按钮）。
INSERT INTO `bp_menu`
  (`id`, `pid`, `name`, `path`, `component`, `redirect`, `type`, `auth_code`, `meta`, `status`)
VALUES
  (900123, 11, 'SystemUserAssetAdjust', '', NULL, NULL, 'button', 'system:user:balance-adjust',
   JSON_OBJECT('title', 'Adjust user balance', 'order', 5), 1)
ON DUPLICATE KEY UPDATE
  `pid` = VALUES(`pid`),
  `type` = VALUES(`type`),
  `auth_code` = VALUES(`auth_code`),
  `meta` = VALUES(`meta`),
  `status` = VALUES(`status`);

INSERT IGNORE INTO `bp_role_menu` (`role_id`, `menu_id`)
VALUES (1, 900123);


-- ============================================================
-- Migration: 000147_currency_display_name.up.sql
-- ============================================================

-- 000147: 用户端货币展示名称。
--
-- 只影响用户端「货币称呼」：把 ¥/人民币 的文案替换为运营自定义名称
-- （如积分、赞助额度）。余额、流水、订单金额仍按人民币基数存储，
-- 汇率换算（usd_cny_rate）照旧只服务于真实货币结算，本字段不参与任何换算。
ALTER TABLE `bp_payment_currency_config`
  ADD COLUMN `display_name` varchar(32) NOT NULL DEFAULT ''
    COMMENT '用户端货币展示名称；空=按结算货币显示' AFTER `goods_name`;


-- Mark database as migrated for golang-migrate.
CREATE TABLE IF NOT EXISTS `schema_migrations` (
  `version` bigint NOT NULL,
  `dirty` tinyint(1) NOT NULL,
  PRIMARY KEY (`version`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
DELETE FROM `schema_migrations`;
INSERT INTO `schema_migrations` (`version`, `dirty`) VALUES (147, 0);

SET FOREIGN_KEY_CHECKS = 1;
DO RELEASE_LOCK(@__bugpk_init_lock);
