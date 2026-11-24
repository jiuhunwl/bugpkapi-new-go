CREATE TABLE IF NOT EXISTS `bp_ticket` (
  `id` bigint NOT NULL AUTO_INCREMENT COMMENT 'Ticket id',
  `ticket_no` varchar(32) NOT NULL COMMENT 'Public ticket number',
  `user_id` bigint NOT NULL COMMENT 'Owner user id',
  `subject` varchar(128) NOT NULL COMMENT 'Ticket subject',
  `category` varchar(32) NOT NULL DEFAULT 'GENERAL' COMMENT 'GENERAL/API/BILLING/ACCOUNT/BUG/OTHER',
  `priority` varchar(16) NOT NULL DEFAULT 'NORMAL' COMMENT 'LOW/NORMAL/HIGH/URGENT',
  `status` varchar(24) NOT NULL DEFAULT 'OPEN' COMMENT 'OPEN/PROCESSING/WAITING_USER/RESOLVED/CLOSED',
  `assigned_to` bigint DEFAULT NULL COMMENT 'Assigned staff user id',
  `last_message_preview` varchar(255) NOT NULL DEFAULT '' COMMENT 'Privacy-safe conversation preview',
  `last_message_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT 'Last message time',
  `last_reply_by` varchar(16) NOT NULL DEFAULT 'USER' COMMENT 'USER/STAFF',
  `message_count` int NOT NULL DEFAULT 1 COMMENT 'Message count',
  `unread_user` int NOT NULL DEFAULT 0 COMMENT 'Unread message count for owner',
  `unread_admin` int NOT NULL DEFAULT 1 COMMENT 'Unread message count for staff',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_bp_ticket_no` (`ticket_no`),
  KEY `idx_bp_ticket_user_status` (`user_id`, `status`, `last_message_at`),
  KEY `idx_bp_ticket_status_priority` (`status`, `priority`, `last_message_at`),
  KEY `idx_bp_ticket_assigned` (`assigned_to`, `status`, `last_message_at`),
  CONSTRAINT `chk_bp_ticket_priority` CHECK (`priority` IN ('LOW','NORMAL','HIGH','URGENT')),
  CONSTRAINT `chk_bp_ticket_status` CHECK (`status` IN ('OPEN','PROCESSING','WAITING_USER','RESOLVED','CLOSED')),
  CONSTRAINT `chk_bp_ticket_reply_by` CHECK (`last_reply_by` IN ('USER','STAFF'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='Support tickets';

CREATE TABLE IF NOT EXISTS `bp_ticket_message` (
  `id` bigint NOT NULL AUTO_INCREMENT COMMENT 'Message id',
  `ticket_id` bigint NOT NULL COMMENT 'Ticket id',
  `sender_id` bigint NOT NULL COMMENT 'Sender user id',
  `sender_type` varchar(16) NOT NULL COMMENT 'USER/STAFF',
  `content` text NOT NULL COMMENT 'Plain-text message body',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  KEY `idx_bp_ticket_message_ticket` (`ticket_id`, `id`),
  KEY `idx_bp_ticket_message_sender` (`sender_id`, `created_at`),
  CONSTRAINT `chk_bp_ticket_message_sender_type` CHECK (`sender_type` IN ('USER','STAFF'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='Ticket conversation messages';

INSERT INTO `bp_menu`
  (`id`, `pid`, `name`, `path`, `component`, `redirect`, `type`, `auth_code`, `meta`, `status`)
VALUES
  (195, 0, 'TicketCenter', '/tickets', '/ticket/index', NULL, 'menu', 'ticket:user:list', JSON_OBJECT('title', 'ticket.title', 'icon', 'lucide:messages-square', 'order', 9988), 1),
  (196, 195, 'TicketCreate', '', NULL, NULL, 'button', 'ticket:user:create', JSON_OBJECT('title', 'ticket.permissions.create', 'order', 1), 1),
  (197, 195, 'TicketReply', '', NULL, NULL, 'button', 'ticket:user:reply', JSON_OBJECT('title', 'ticket.permissions.reply', 'order', 2), 1),
  (198, 195, 'TicketAdminList', '', NULL, NULL, 'button', 'ticket:admin:list', JSON_OBJECT('title', 'ticket.permissions.adminList', 'order', 3), 1),
  (199, 195, 'TicketAdminReply', '', NULL, NULL, 'button', 'ticket:admin:reply', JSON_OBJECT('title', 'ticket.permissions.adminReply', 'order', 4), 1),
  (200, 195, 'TicketAdminUpdate', '', NULL, NULL, 'button', 'ticket:admin:update', JSON_OBJECT('title', 'ticket.permissions.adminUpdate', 'order', 5), 1)
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
SELECT 1, `id` FROM `bp_menu` WHERE `id` BETWEEN 195 AND 200;

INSERT IGNORE INTO `bp_role_menu` (`role_id`, `menu_id`)
SELECT 2, `id` FROM `bp_menu` WHERE `id` BETWEEN 195 AND 197;
