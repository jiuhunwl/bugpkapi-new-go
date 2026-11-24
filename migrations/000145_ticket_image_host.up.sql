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
