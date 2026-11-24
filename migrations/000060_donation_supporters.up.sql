CREATE TABLE IF NOT EXISTS `bp_donation_supporter` (
  `id` bigint NOT NULL AUTO_INCREMENT,
  `nickname` varchar(64) NOT NULL COMMENT 'Public supporter nickname',
  `avatar_url` varchar(1024) NOT NULL DEFAULT '' COMMENT 'Public http/https avatar URL',
  `amount` decimal(12,2) NOT NULL COMMENT 'Donation amount in CNY',
  `message` varchar(255) NOT NULL DEFAULT '' COMMENT 'Optional public supporter message',
  `donated_at` datetime NOT NULL COMMENT 'Actual donation time shown publicly',
  `created_by` bigint DEFAULT NULL COMMENT 'Administrator who added this supporter',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  KEY `idx_bp_donation_supporter_time` (`donated_at`,`id`),
  KEY `idx_bp_donation_supporter_creator` (`created_by`),
  CONSTRAINT `chk_bp_donation_supporter_amount` CHECK (`amount` > 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='Public donation supporter list';

INSERT INTO `bp_menu`
  (`id`,`pid`,`name`,`path`,`component`,`redirect`,`type`,`auth_code`,`meta`,`status`)
VALUES
  (193,190,'DonationSupporterList','',NULL,NULL,'button','donation:supporter:list',JSON_OBJECT('title','查看赞赏人','order',3),1),
  (194,190,'DonationSupporterCreate','',NULL,NULL,'button','donation:supporter:create',JSON_OBJECT('title','新增赞赏人','order',4),1)
ON DUPLICATE KEY UPDATE
  `pid`=VALUES(`pid`),`path`=VALUES(`path`),`component`=VALUES(`component`),
  `redirect`=VALUES(`redirect`),`type`=VALUES(`type`),`auth_code`=VALUES(`auth_code`),
  `meta`=VALUES(`meta`),`status`=VALUES(`status`);

INSERT IGNORE INTO `bp_role_menu` (`role_id`,`menu_id`)
SELECT 1,`id` FROM `bp_menu` WHERE `id` BETWEEN 193 AND 194;
