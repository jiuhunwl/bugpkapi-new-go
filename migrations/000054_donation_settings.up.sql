CREATE TABLE IF NOT EXISTS `bp_donation_config` (
  `id` bigint NOT NULL DEFAULT 1 COMMENT 'Singleton id; always 1',
  `enabled` tinyint NOT NULL DEFAULT 0 COMMENT 'Whether the public donation page is published',
  `eyebrow` varchar(64) NOT NULL DEFAULT '赞赏',
  `title` varchar(120) NOT NULL DEFAULT '赞赏支持',
  `description` varchar(500) NOT NULL DEFAULT '',
  `scan_title` varchar(120) NOT NULL DEFAULT '扫码赞赏',
  `scan_description` varchar(255) NOT NULL DEFAULT '',
  `thanks` varchar(255) NOT NULL DEFAULT '',
  `methods` json NOT NULL COMMENT 'Ordered donation methods and QR-code settings',
  `updated_by` bigint DEFAULT NULL,
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  CONSTRAINT `chk_bp_donation_config_singleton` CHECK (`id` = 1)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='Donation page singleton config';

INSERT INTO `bp_donation_config`
  (`id`,`enabled`,`eyebrow`,`title`,`description`,`scan_title`,`scan_description`,`thanks`,`methods`)
VALUES
  (1,0,'赞赏','赞赏支持','如果 BugPk-Api 对你有帮助，欢迎用你喜欢的方式支持项目。','扫码赞赏','选择你习惯的支付方式，感谢每一份支持。','感谢你的支持，我们会继续把服务做好。',JSON_ARRAY())
ON DUPLICATE KEY UPDATE `id`=VALUES(`id`);

INSERT INTO `bp_menu`
  (`id`,`pid`,`name`,`path`,`component`,`redirect`,`type`,`auth_code`,`meta`,`status`)
VALUES
  (190,120,'DonationConfig','/content/donation','/donation/index',NULL,'menu','donation:config:view',JSON_OBJECT('title','赞赏设置','icon','lucide:heart-handshake','order',8),1),
  (191,190,'DonationConfigUpdate','',NULL,NULL,'button','donation:config:update',JSON_OBJECT('title','保存赞赏设置','order',1),1),
  (192,190,'DonationQRCodeUpload','',NULL,NULL,'button','donation:upload-qr',JSON_OBJECT('title','上传赞赏收款码','order',2),1)
ON DUPLICATE KEY UPDATE
  `pid`=VALUES(`pid`),`path`=VALUES(`path`),`component`=VALUES(`component`),
  `redirect`=VALUES(`redirect`),`type`=VALUES(`type`),`auth_code`=VALUES(`auth_code`),
  `meta`=VALUES(`meta`),`status`=VALUES(`status`);

INSERT IGNORE INTO `bp_role_menu` (`role_id`,`menu_id`)
SELECT 1,`id` FROM `bp_menu` WHERE `id` BETWEEN 190 AND 192;
