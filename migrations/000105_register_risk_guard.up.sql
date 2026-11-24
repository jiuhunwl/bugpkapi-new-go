-- Register risk guard (anti bulk-registration).
--
-- 1) bp_auth_register_config.register_ip_max_accounts caps how many accounts
--    may share one registration IP (0 = disabled, preserves legacy behavior).
--    The per-IP *rate* limit (register_ip_hour_limit) throttles attempts; this
--    new column is a hard lifetime cap enforced by bp_register_ip_used.
-- 2) bp_email_domain_rule holds the admin-managed email domain policy:
--    `whitelist` rows allow-list the only domains that may register (a
--    non-empty whitelist makes registration exclusive to those domains) and
--    `blacklist` rows reject disposable/abused domains outright.
-- 3) bp_register_ip_used records one row per account created from an IP. The
--    primary key on ip_hash makes the 1-account-per-IP cap race-free at the
--    storage layer; ip_hash is iphash.RiskDigest(client_ip) so the table never
--    stores a plaintext IP.
ALTER TABLE `bp_auth_register_config`
  ADD COLUMN `register_ip_max_accounts` int NOT NULL DEFAULT 0
    COMMENT 'Lifetime cap of accounts per registration IP; 0 disables the cap'
    AFTER `register_ip_hour_limit`;

CREATE TABLE `bp_email_domain_rule` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `rule_kind` varchar(16) NOT NULL COMMENT 'whitelist or blacklist',
  `domain` varchar(255) NOT NULL COMMENT 'lower-case email domain, no @',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_email_domain_rule` (`rule_kind`, `domain`),
  CONSTRAINT `chk_email_domain_rule_kind` CHECK (`rule_kind` in ('whitelist', 'blacklist'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='Registration email domain whitelist/blacklist';

CREATE TABLE `bp_register_ip_used` (
  `ip_hash` binary(32) NOT NULL COMMENT 'iphash.RiskDigest(client_ip) at registration time',
  `user_id` bigint unsigned NOT NULL,
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (`ip_hash`),
  KEY `idx_register_ip_user` (`user_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='One row per account created from a registration IP (keyed hash, no plaintext IP)';
