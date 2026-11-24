CREATE TABLE IF NOT EXISTS `bp_notification_outbox` (
  `id` bigint NOT NULL AUTO_INCREMENT,
  `channel` varchar(24) NOT NULL DEFAULT 'EMAIL',
  `template_key` varchar(80) NOT NULL,
  `recipient` varchar(255) NOT NULL,
  `language` varchar(8) NOT NULL DEFAULT 'zh',
  `payload` json NOT NULL,
  `dedupe_key` varchar(191) NOT NULL,
  `status` varchar(24) NOT NULL DEFAULT 'PENDING',
  `attempts` int NOT NULL DEFAULT 0,
  `max_attempts` int NOT NULL DEFAULT 12,
  `next_attempt_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `locked_at` datetime DEFAULT NULL,
  `last_error` varchar(1000) NOT NULL DEFAULT '',
  `sent_at` datetime DEFAULT NULL,
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_bp_notification_outbox_dedupe` (`dedupe_key`),
  KEY `idx_bp_notification_outbox_pending` (`status`, `next_attempt_at`, `id`),
  KEY `idx_bp_notification_outbox_recipient` (`recipient`, `created_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='Asynchronous notification outbox';

