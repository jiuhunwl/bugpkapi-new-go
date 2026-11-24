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
