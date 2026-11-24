-- ============================================================
-- Migration 000064: add open-source branding columns to bp_site_config
-- Adds repository, author, and license fields for dynamic site branding.
-- ============================================================

ALTER TABLE `bp_site_config`
  ADD COLUMN `github_url` varchar(1024) NOT NULL DEFAULT '' COMMENT 'GitHub repository URL' AFTER `site_url`,
  ADD COLUMN `author_name` varchar(128) NOT NULL DEFAULT '' COMMENT 'Open-source author or organization name' AFTER `github_url`,
  ADD COLUMN `author_url` varchar(1024) NOT NULL DEFAULT '' COMMENT 'Author homepage or profile URL' AFTER `author_name`,
  ADD COLUMN `license_type` varchar(64) NOT NULL DEFAULT 'AGPL-3.0' COMMENT 'Open-source license type (e.g. MIT, Apache-2.0)' AFTER `author_url`,
  ADD COLUMN `license_url` varchar(1024) NOT NULL DEFAULT '' COMMENT 'Full license text URL' AFTER `license_type`;

-- 仓库地址由部署者在后台填写，仓库地址与作者链接不写死在迁移里。
UPDATE `bp_site_config`
SET `author_name` = 'BugPk-Api',
    `license_type` = 'AGPL-3.0'
WHERE `id` = 1;
