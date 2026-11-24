CREATE TABLE IF NOT EXISTS `bp_notice` (
  `id` bigint NOT NULL AUTO_INCREMENT,
  `title` varchar(100) NOT NULL,
  `summary` varchar(255) NOT NULL DEFAULT '',
  `content_html` mediumtext NOT NULL,
  `is_top` tinyint NOT NULL DEFAULT 0,
  `is_popup` tinyint NOT NULL DEFAULT 0,
  `status` tinyint NOT NULL DEFAULT 1,
  `publish_time` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `creator_id` bigint DEFAULT NULL,
  `updater_id` bigint DEFAULT NULL,
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  KEY `idx_bp_notice_public` (`status`, `is_top`, `publish_time`, `id`),
  KEY `idx_bp_notice_popup` (`status`, `is_popup`, `publish_time`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='Content notice';

CREATE TABLE IF NOT EXISTS `bp_home_notice_config` (
  `id` bigint NOT NULL DEFAULT 1,
  `enabled` tinyint NOT NULL DEFAULT 0,
  `content` varchar(300) NOT NULL DEFAULT '',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='Home rolling notice config';

INSERT INTO `bp_home_notice_config` (`id`, `enabled`, `content`)
VALUES (1, 0, '')
ON DUPLICATE KEY UPDATE `id` = `id`;

CREATE TABLE IF NOT EXISTS `bp_site_config` (
  `id` bigint NOT NULL DEFAULT 1,
  `site_name` varchar(64) NOT NULL DEFAULT 'BugPk-Api',
  `logo_url` varchar(1024) NOT NULL DEFAULT '',
  `slogan` varchar(120) NOT NULL DEFAULT '',
  `description` varchar(255) NOT NULL DEFAULT '',
  `contact_email` varchar(128) NOT NULL DEFAULT '',
  `contact_phone` varchar(64) NOT NULL DEFAULT '',
  `contact_qq` varchar(64) NOT NULL DEFAULT '',
  `contact_wechat` varchar(64) NOT NULL DEFAULT '',
  `contact_address` varchar(255) NOT NULL DEFAULT '',
  `icp` varchar(128) NOT NULL DEFAULT '',
  `copyright` varchar(128) NOT NULL DEFAULT '',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='Site config singleton';

INSERT INTO `bp_site_config` (`id`, `site_name`, `slogan`, `description`, `copyright`)
VALUES (1, 'BugPk-Api', 'Open API service platform', 'BugPk-Api service platform', 'Copyright BugPk-Api')
ON DUPLICATE KEY UPDATE `id` = `id`;

CREATE TABLE IF NOT EXISTS `bp_friend_link` (
  `id` bigint NOT NULL AUTO_INCREMENT,
  `site_name` varchar(100) NOT NULL,
  `site_url` varchar(1024) NOT NULL,
  `normalized_site_url` varchar(1024) NOT NULL,
  `logo_url` varchar(1024) NOT NULL DEFAULT '',
  `description` varchar(255) NOT NULL DEFAULT '',
  `sort_no` int NOT NULL DEFAULT 0,
  `status` tinyint NOT NULL DEFAULT 1,
  `applicant_name` varchar(64) NOT NULL DEFAULT '',
  `applicant_email` varchar(128) NOT NULL DEFAULT '',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_bp_friend_link_normalized_url` (`normalized_site_url`(191)),
  KEY `idx_bp_friend_link_public` (`status`, `sort_no`, `id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='Friend link';

CREATE TABLE IF NOT EXISTS `bp_friend_link_application` (
  `id` bigint NOT NULL AUTO_INCREMENT,
  `user_id` bigint NOT NULL,
  `username` varchar(64) NOT NULL DEFAULT '',
  `site_name` varchar(100) NOT NULL,
  `site_url` varchar(1024) NOT NULL,
  `normalized_site_url` varchar(1024) NOT NULL,
  `logo_url` varchar(1024) NOT NULL DEFAULT '',
  `description` varchar(255) NOT NULL DEFAULT '',
  `contact_name` varchar(64) NOT NULL DEFAULT '',
  `contact_email` varchar(128) NOT NULL DEFAULT '',
  `contact_qq` varchar(64) NOT NULL DEFAULT '',
  `backlink_url` varchar(1024) NOT NULL DEFAULT '',
  `status` varchar(16) NOT NULL DEFAULT 'PENDING',
  `reject_reason` varchar(255) NOT NULL DEFAULT '',
  `reviewer_id` bigint DEFAULT NULL,
  `reviewer_name` varchar(64) NOT NULL DEFAULT '',
  `review_time` datetime DEFAULT NULL,
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  KEY `idx_bp_friend_link_app_user` (`user_id`, `created_at`),
  KEY `idx_bp_friend_link_app_status` (`status`, `created_at`),
  KEY `idx_bp_friend_link_app_url` (`normalized_site_url`(191), `status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='Friend link application';

CREATE TABLE IF NOT EXISTS `bp_friend_link_config` (
  `id` bigint NOT NULL DEFAULT 1,
  `apply_enabled` tinyint NOT NULL DEFAULT 1,
  `apply_notice` varchar(500) NOT NULL DEFAULT '',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='Friend link config';

INSERT INTO `bp_friend_link_config` (`id`, `apply_enabled`, `apply_notice`)
VALUES (1, 1, '')
ON DUPLICATE KEY UPDATE `id` = `id`;

INSERT INTO `bp_menu`
  (`id`, `pid`, `name`, `path`, `component`, `redirect`, `type`, `auth_code`, `meta`, `status`)
VALUES
  (120, 0, 'ContentOperations', '/content', NULL, NULL, 'catalog', 'content:root', JSON_OBJECT('title', '内容运营', 'icon', 'lucide:newspaper', 'order', 9990), 1),
  (121, 120, 'NoticeList', '/content/notices', '/notice/admin/list', NULL, 'menu', 'notice:list', JSON_OBJECT('title', '公告管理', 'order', 1), 1),
  (122, 121, 'NoticeCreate', '', NULL, NULL, 'button', 'notice:create', JSON_OBJECT('title', '新增公告'), 1),
  (123, 121, 'NoticeUpdate', '', NULL, NULL, 'button', 'notice:update', JSON_OBJECT('title', '编辑公告'), 1),
  (124, 121, 'NoticeDelete', '', NULL, NULL, 'button', 'notice:delete', JSON_OBJECT('title', '删除公告'), 1),
  (125, 121, 'NoticeStatus', '', NULL, NULL, 'button', 'notice:status', JSON_OBJECT('title', '公告状态'), 1),
  (126, 121, 'NoticeTop', '', NULL, NULL, 'button', 'notice:top', JSON_OBJECT('title', '公告置顶'), 1),
  (127, 121, 'NoticePopup', '', NULL, NULL, 'button', 'notice:popup', JSON_OBJECT('title', '弹窗公告'), 1),
  (128, 120, 'HomeNoticeConfig', '/content/home-notice', '/notice/admin/home-notice/index', NULL, 'menu', 'notice:home-config', JSON_OBJECT('title', '首页滚动公告', 'order', 2), 1),
  (129, 121, 'NoticeImageUpload', '', NULL, NULL, 'button', 'notice:upload-image', JSON_OBJECT('title', '上传公告图片'), 1),
  (130, 120, 'SiteConfig', '/content/site', '/system/site/index', NULL, 'menu', 'site:config:view', JSON_OBJECT('title', '站点配置', 'order', 3), 1),
  (131, 130, 'SiteConfigUpdate', '', NULL, NULL, 'button', 'site:config:update', JSON_OBJECT('title', '更新站点配置'), 1),
  (132, 130, 'SiteLogoUpload', '', NULL, NULL, 'button', 'site:upload-logo', JSON_OBJECT('title', '上传站点Logo'), 1),
  (133, 120, 'FriendLinkList', '/content/friend-links', '/friend-link/list', NULL, 'menu', 'friend-link:list', JSON_OBJECT('title', '友情链接', 'order', 4), 1),
  (134, 133, 'FriendLinkCreate', '', NULL, NULL, 'button', 'friend-link:create', JSON_OBJECT('title', '新增友情链接'), 1),
  (135, 133, 'FriendLinkUpdate', '', NULL, NULL, 'button', 'friend-link:update', JSON_OBJECT('title', '编辑友情链接'), 1),
  (136, 133, 'FriendLinkDelete', '', NULL, NULL, 'button', 'friend-link:delete', JSON_OBJECT('title', '删除友情链接'), 1),
  (137, 133, 'FriendLinkStatus', '', NULL, NULL, 'button', 'friend-link:status', JSON_OBJECT('title', '友情链接状态'), 1),
  (138, 120, 'FriendLinkApplications', '/content/friend-link-applications', '/friend-link/applications', NULL, 'menu', 'friend-link:application:list', JSON_OBJECT('title', '友链申请', 'order', 5), 1),
  (139, 138, 'FriendLinkApplicationReview', '', NULL, NULL, 'button', 'friend-link:application:review', JSON_OBJECT('title', '审核友链申请'), 1),
  (140, 120, 'FriendLinkConfig', '/content/friend-link-config', '/friend-link/config', NULL, 'menu', 'friend-link:config:update', JSON_OBJECT('title', '友链配置', 'order', 6), 1),
  (141, 0, 'HomeOverview', '/home', NULL, NULL, 'catalog', 'home:overview:view', JSON_OBJECT('title', '首页概览', 'icon', 'lucide:layout-dashboard', 'order', 9989), 1)
ON DUPLICATE KEY UPDATE
  `pid` = VALUES(`pid`),
  `path` = VALUES(`path`),
  `component` = VALUES(`component`),
  `type` = VALUES(`type`),
  `auth_code` = VALUES(`auth_code`),
  `meta` = VALUES(`meta`),
  `status` = VALUES(`status`);

INSERT IGNORE INTO `bp_role_menu` (`role_id`, `menu_id`)
SELECT 1, id FROM `bp_menu` WHERE id BETWEEN 120 AND 141;
