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
