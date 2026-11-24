CREATE TABLE IF NOT EXISTS `bp_captcha_global_config` (
  `id` tinyint NOT NULL DEFAULT 1 COMMENT 'Singleton id',
  `enabled` tinyint NOT NULL DEFAULT 1 COMMENT 'Global captcha switch: 1 enabled, 0 disabled',
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  CONSTRAINT `chk_bp_captcha_global_singleton` CHECK (`id` = 1),
  CONSTRAINT `chk_bp_captcha_global_enabled` CHECK (`enabled` IN (0, 1))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='Global captcha switch';

INSERT INTO `bp_captcha_global_config` (`id`, `enabled`)
VALUES (1, 1)
ON DUPLICATE KEY UPDATE `id` = VALUES(`id`);
