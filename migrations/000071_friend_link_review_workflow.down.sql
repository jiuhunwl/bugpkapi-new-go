DELETE FROM `bp_captcha_provider_config` WHERE `scene`='friend_link_apply';

DROP TABLE IF EXISTS `bp_friend_link_audit`;

ALTER TABLE `bp_friend_link`
  DROP KEY `idx_bp_friend_link_source_application`,
  DROP COLUMN `source_application_id`,
  DROP COLUMN `language`;

ALTER TABLE `bp_friend_link_application`
  MODIFY COLUMN `reject_reason` varchar(255) NOT NULL DEFAULT '',
  DROP COLUMN `review_send_email`,
  DROP COLUMN `review_reason_template`,
  DROP COLUMN `language`;
