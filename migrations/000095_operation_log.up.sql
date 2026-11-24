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
