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
