ALTER TABLE `bp_ticket`
  ADD COLUMN `tags` json DEFAULT NULL COMMENT 'Administrator-maintained ticket labels' AFTER `assigned_to`,
  ADD COLUMN `language` varchar(8) NOT NULL DEFAULT 'zh' COMMENT 'Notification language snapshot' AFTER `tags`,
  ADD COLUMN `first_response_due_at` datetime DEFAULT NULL COMMENT 'First staff response SLA deadline' AFTER `language`,
  ADD COLUMN `resolution_due_at` datetime DEFAULT NULL COMMENT 'Resolution SLA deadline' AFTER `first_response_due_at`,
  ADD COLUMN `first_responded_at` datetime DEFAULT NULL COMMENT 'First staff response time' AFTER `resolution_due_at`,
  ADD COLUMN `resolved_at` datetime DEFAULT NULL COMMENT 'First resolved time' AFTER `first_responded_at`,
  ADD COLUMN `closed_at` datetime DEFAULT NULL COMMENT 'Closed time' AFTER `resolved_at`,
  ADD KEY `idx_bp_ticket_first_response_sla` (`first_response_due_at`, `first_responded_at`, `status`),
  ADD KEY `idx_bp_ticket_resolution_sla` (`resolution_due_at`, `resolved_at`, `status`);

ALTER TABLE `bp_ticket_message`
  ADD COLUMN `internal_note` tinyint NOT NULL DEFAULT 0 COMMENT 'Staff-only internal note' AFTER `sender_type`,
  ADD KEY `idx_bp_ticket_message_visibility` (`ticket_id`, `internal_note`, `id`);

UPDATE `bp_ticket`
SET `first_response_due_at` = CASE `priority`
      WHEN 'URGENT' THEN DATE_ADD(`created_at`, INTERVAL 15 MINUTE)
      WHEN 'HIGH' THEN DATE_ADD(`created_at`, INTERVAL 1 HOUR)
      WHEN 'LOW' THEN DATE_ADD(`created_at`, INTERVAL 8 HOUR)
      ELSE DATE_ADD(`created_at`, INTERVAL 4 HOUR)
    END,
    `resolution_due_at` = CASE `priority`
      WHEN 'URGENT' THEN DATE_ADD(`created_at`, INTERVAL 4 HOUR)
      WHEN 'HIGH' THEN DATE_ADD(`created_at`, INTERVAL 12 HOUR)
      WHEN 'LOW' THEN DATE_ADD(`created_at`, INTERVAL 72 HOUR)
      ELSE DATE_ADD(`created_at`, INTERVAL 48 HOUR)
    END,
    `resolved_at` = CASE WHEN `status` IN ('RESOLVED', 'CLOSED') THEN `updated_at` ELSE NULL END,
    `closed_at` = CASE WHEN `status` = 'CLOSED' THEN `updated_at` ELSE NULL END
WHERE `first_response_due_at` IS NULL OR `resolution_due_at` IS NULL;

CREATE TABLE IF NOT EXISTS `bp_ticket_attachment` (
  `id` bigint NOT NULL AUTO_INCREMENT,
  `ticket_id` bigint NOT NULL,
  `message_id` bigint DEFAULT NULL,
  `uploader_id` bigint NOT NULL,
  `original_name` varchar(255) NOT NULL,
  `stored_name` varchar(96) NOT NULL,
  `content_type` varchar(128) NOT NULL,
  `size_bytes` bigint NOT NULL,
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_bp_ticket_attachment_stored` (`stored_name`),
  KEY `idx_bp_ticket_attachment_ticket` (`ticket_id`, `id`),
  KEY `idx_bp_ticket_attachment_message` (`message_id`, `id`),
  KEY `idx_bp_ticket_attachment_uploader` (`uploader_id`, `created_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='Private ticket attachments';

CREATE TABLE IF NOT EXISTS `bp_ticket_activity` (
  `id` bigint NOT NULL AUTO_INCREMENT,
  `ticket_id` bigint NOT NULL,
  `actor_id` bigint NOT NULL,
  `actor_type` varchar(16) NOT NULL COMMENT 'USER/STAFF/SYSTEM',
  `action` varchar(48) NOT NULL,
  `visibility` varchar(16) NOT NULL DEFAULT 'PUBLIC' COMMENT 'PUBLIC/INTERNAL',
  `detail` json DEFAULT NULL,
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  KEY `idx_bp_ticket_activity_ticket` (`ticket_id`, `id`),
  KEY `idx_bp_ticket_activity_action` (`action`, `created_at`),
  CONSTRAINT `chk_bp_ticket_activity_visibility` CHECK (`visibility` IN ('PUBLIC','INTERNAL'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='Ticket activity audit trail';

CREATE TABLE IF NOT EXISTS `bp_ticket_quick_reply` (
  `id` bigint NOT NULL AUTO_INCREMENT,
  `title` varchar(80) NOT NULL,
  `content_zh` text NOT NULL,
  `content_en` text NOT NULL,
  `category` varchar(32) NOT NULL DEFAULT 'GENERAL',
  `sort_no` int NOT NULL DEFAULT 0,
  `status` tinyint NOT NULL DEFAULT 1,
  `created_by` bigint DEFAULT NULL,
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  KEY `idx_bp_ticket_quick_reply_status` (`status`, `sort_no`, `id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='Administrator ticket quick replies';

INSERT INTO `bp_ticket_quick_reply`
  (`id`, `title`, `content_zh`, `content_en`, `category`, `sort_no`, `status`)
VALUES
  (1, '需要更多信息', '您好，为了进一步定位问题，请补充接口名称、请求时间、错误信息和可复现步骤，并注意不要提交完整 API Key。', 'Hello. To investigate further, please provide the API name, request time, error message, and reproduction steps. Do not include a complete API key.', 'GENERAL', 10, 1),
  (2, '问题已处理', '您好，相关问题已经处理，请重新尝试。如仍有异常，请在本工单中继续回复。', 'Hello. The issue has been addressed. Please try again and reply to this ticket if the problem continues.', 'GENERAL', 20, 1),
  (3, '等待用户确认', '您好，当前已完成初步处理，请您确认问题是否恢复；确认后我们会关闭工单。', 'Hello. Initial remediation is complete. Please confirm whether the issue is resolved so we can close the ticket.', 'GENERAL', 30, 1)
ON DUPLICATE KEY UPDATE
  `title` = VALUES(`title`),
  `content_zh` = VALUES(`content_zh`),
  `content_en` = VALUES(`content_en`),
  `category` = VALUES(`category`),
  `sort_no` = VALUES(`sort_no`),
  `status` = VALUES(`status`);

INSERT INTO `bp_menu`
  (`id`, `pid`, `name`, `path`, `component`, `redirect`, `type`, `auth_code`, `meta`, `status`)
VALUES
  (205, 195, 'TicketUserClose', '', NULL, NULL, 'button', 'ticket:user:close', JSON_OBJECT('title', 'ticket.permissions.close', 'order', 3), 1),
  (206, 195, 'TicketUserAttachment', '', NULL, NULL, 'button', 'ticket:user:attachment', JSON_OBJECT('title', 'ticket.permissions.attachment', 'order', 4), 1),
  (207, 204, 'TicketAdminNote', '', NULL, NULL, 'button', 'ticket:admin:note', JSON_OBJECT('title', 'ticket.permissions.adminNote', 'order', 4), 1),
  (208, 204, 'TicketAdminStats', '', NULL, NULL, 'button', 'ticket:admin:stats', JSON_OBJECT('title', 'ticket.permissions.adminStats', 'order', 5), 1),
  (209, 204, 'TicketAdminQuickReply', '', NULL, NULL, 'button', 'ticket:admin:quick-reply', JSON_OBJECT('title', 'ticket.permissions.adminQuickReply', 'order', 6), 1),
  (210, 204, 'TicketAdminAttachment', '', NULL, NULL, 'button', 'ticket:admin:attachment', JSON_OBJECT('title', 'ticket.permissions.attachment', 'order', 7), 1)
ON DUPLICATE KEY UPDATE
  `pid` = VALUES(`pid`),
  `type` = VALUES(`type`),
  `auth_code` = VALUES(`auth_code`),
  `meta` = VALUES(`meta`),
  `status` = VALUES(`status`);

INSERT IGNORE INTO `bp_role_menu` (`role_id`, `menu_id`)
SELECT r.`id`, m.`id`
FROM `bp_role` r
JOIN `bp_menu` m ON m.`id` IN (205, 206)
WHERE r.`role_key` = 'user';

INSERT IGNORE INTO `bp_role_menu` (`role_id`, `menu_id`)
SELECT r.`id`, m.`id`
FROM `bp_role` r
JOIN `bp_menu` m ON m.`id` BETWEEN 207 AND 210
WHERE r.`role_key` = 'admin';
