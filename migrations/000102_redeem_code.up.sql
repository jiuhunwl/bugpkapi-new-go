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
