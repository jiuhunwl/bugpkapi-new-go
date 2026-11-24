-- 站内消息中心（全站对用户的通知）。
--
-- 管理员可向全部用户或指定用户发送站内消息；每用户一行，便于各自的
-- 已读状态。可选同时通过统一邮件 outbox 向绑定邮箱的用户发送邮件副本。

CREATE TABLE IF NOT EXISTS `bp_user_inbox` (
  `id` bigint NOT NULL AUTO_INCREMENT,
  `user_id` bigint NOT NULL COMMENT '接收用户',
  `title` varchar(128) NOT NULL,
  `body` varchar(4000) NOT NULL DEFAULT '' COMMENT '纯文本正文',
  `kind` varchar(32) NOT NULL DEFAULT 'SYSTEM' COMMENT 'SYSTEM=管理员群发',
  `is_read` tinyint NOT NULL DEFAULT 0,
  `read_at` datetime DEFAULT NULL,
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  KEY `idx_bp_user_inbox_user` (`user_id`, `is_read`, `id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='站内消息';

-- 管理端：全站通知发送页
INSERT INTO `bp_menu`
  (`id`, `pid`, `name`, `path`, `component`, `redirect`, `type`, `auth_code`, `meta`, `status`)
VALUES
  (241, 30, 'SystemInboxBroadcast', '/system/inbox-broadcast', '/system/inbox-broadcast/index', NULL, 'menu', 'system:inbox:broadcast', JSON_OBJECT('title', 'inbox.broadcastTitle', 'icon', 'lucide:megaphone', 'order', 9), 1)
ON DUPLICATE KEY UPDATE
  `pid` = VALUES(`pid`),
  `path` = VALUES(`path`),
  `component` = VALUES(`component`),
  `redirect` = VALUES(`redirect`),
  `type` = VALUES(`type`),
  `auth_code` = VALUES(`auth_code`),
  `meta` = VALUES(`meta`),
  `status` = VALUES(`status`);

-- 用户端：消息中心
INSERT INTO `bp_menu`
  (`id`, `pid`, `name`, `path`, `component`, `redirect`, `type`, `auth_code`, `meta`, `status`)
VALUES
  (242, 84, 'UserInbox', '/user/inbox', '/user/inbox/index', NULL, 'menu', 'inbox:self:read', JSON_OBJECT('title', 'inbox.userTitle', 'icon', 'lucide:bell', 'order', 9), 1)
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
SELECT 1, `id` FROM `bp_menu` WHERE `id` = 241;

INSERT IGNORE INTO `bp_role_menu` (`role_id`, `menu_id`)
SELECT `id`, 242 FROM `bp_role` WHERE `role_key` IN ('admin', 'user');
