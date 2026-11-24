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
