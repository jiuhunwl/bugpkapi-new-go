DELETE FROM `bp_role_menu` WHERE `menu_id` BETWEEN 205 AND 210;
DELETE FROM `bp_menu` WHERE `id` BETWEEN 205 AND 210;

DROP TABLE IF EXISTS `bp_ticket_quick_reply`;
DROP TABLE IF EXISTS `bp_ticket_activity`;
DROP TABLE IF EXISTS `bp_ticket_attachment`;

ALTER TABLE `bp_ticket_message`
  DROP KEY `idx_bp_ticket_message_visibility`,
  DROP COLUMN `internal_note`;

ALTER TABLE `bp_ticket`
  DROP KEY `idx_bp_ticket_first_response_sla`,
  DROP KEY `idx_bp_ticket_resolution_sla`,
  DROP COLUMN `closed_at`,
  DROP COLUMN `resolved_at`,
  DROP COLUMN `first_responded_at`,
  DROP COLUMN `resolution_due_at`,
  DROP COLUMN `first_response_due_at`,
  DROP COLUMN `language`,
  DROP COLUMN `tags`;
