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
