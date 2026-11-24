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
