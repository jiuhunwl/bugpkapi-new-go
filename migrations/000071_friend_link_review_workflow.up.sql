ALTER TABLE `bp_friend_link_application`
  MODIFY COLUMN `reject_reason` varchar(1000) NOT NULL DEFAULT '',
  ADD COLUMN `language` varchar(8) NOT NULL DEFAULT 'zh' COMMENT 'Applicant page language snapshot' AFTER `backlink_url`,
  ADD COLUMN `review_reason_template` varchar(64) NOT NULL DEFAULT '' COMMENT 'Review reason template key' AFTER `reject_reason`,
  ADD COLUMN `review_send_email` tinyint NOT NULL DEFAULT 1 COMMENT 'Whether review email was requested' AFTER `review_reason_template`;

ALTER TABLE `bp_friend_link`
  ADD COLUMN `language` varchar(8) NOT NULL DEFAULT 'zh' COMMENT 'Applicant language snapshot' AFTER `applicant_email`,
  ADD COLUMN `source_application_id` bigint DEFAULT NULL COMMENT 'Source application id' AFTER `language`,
  ADD KEY `idx_bp_friend_link_source_application` (`source_application_id`);

CREATE TABLE IF NOT EXISTS `bp_friend_link_audit` (
  `id` bigint NOT NULL AUTO_INCREMENT,
  `entity_type` varchar(24) NOT NULL COMMENT 'APPLICATION/LINK',
  `entity_id` bigint NOT NULL,
  `action` varchar(24) NOT NULL COMMENT 'APPLY/APPROVE/REJECT/DELETE',
  `actor_id` bigint DEFAULT NULL,
  `actor_name` varchar(64) NOT NULL DEFAULT '',
  `reason` varchar(1000) NOT NULL DEFAULT '',
  `reason_template` varchar(64) NOT NULL DEFAULT '',
  `send_email` tinyint NOT NULL DEFAULT 0,
  `language` varchar(8) NOT NULL DEFAULT 'zh',
  `snapshot` json DEFAULT NULL,
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  KEY `idx_bp_friend_link_audit_entity` (`entity_type`, `entity_id`, `id`),
  KEY `idx_bp_friend_link_audit_actor` (`actor_id`, `created_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='Friend link application and removal audit trail';

INSERT INTO `bp_captcha_provider_config`
  (`provider_key`, `scene`, `enabled`, `timeout_ms`, `failure_policy`, `extra_config`)
VALUES
  ('pow', 'friend_link_apply', 1, 3000, 'deny', JSON_OBJECT('difficulty', 3))
ON DUPLICATE KEY UPDATE
  `enabled` = VALUES(`enabled`),
  `extra_config` = VALUES(`extra_config`);
