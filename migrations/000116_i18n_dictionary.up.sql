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
