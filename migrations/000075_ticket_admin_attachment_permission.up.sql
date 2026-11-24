-- Migration 000069 temporarily assigned menu id 210 to the administrator
-- ticket-attachment permission. Migration 000072 later reused that id for the
-- user support catalog, so restore the administrator permission under a
-- collision-free id.
INSERT INTO `bp_menu`
  (`pid`, `name`, `path`, `component`, `redirect`, `type`, `auth_code`, `meta`, `status`)
SELECT
  admin_ticket.`id`, 'TicketAdminAttachment', '', NULL, NULL, 'button',
  'ticket:admin:attachment',
  JSON_OBJECT('title', 'ticket.permissions.attachment', 'order', 7),
  1
FROM `bp_menu` admin_ticket
WHERE admin_ticket.`name` = 'AdminTicketManagement'
  AND NOT EXISTS (
    SELECT 1 FROM `bp_menu` existing
    WHERE existing.`name` = 'TicketAdminAttachment'
  );

UPDATE `bp_menu` attachment
JOIN `bp_menu` admin_ticket ON admin_ticket.`name` = 'AdminTicketManagement'
SET attachment.`pid` = admin_ticket.`id`,
    attachment.`path` = '',
    attachment.`component` = NULL,
    attachment.`redirect` = NULL,
    attachment.`type` = 'button',
    attachment.`auth_code` = 'ticket:admin:attachment',
    attachment.`meta` = JSON_OBJECT('title', 'ticket.permissions.attachment', 'order', 7),
    attachment.`status` = 1
WHERE attachment.`name` = 'TicketAdminAttachment';

INSERT IGNORE INTO `bp_role_menu` (`role_id`, `menu_id`)
SELECT r.`id`, m.`id`
FROM `bp_role` r
JOIN `bp_menu` m ON m.`name` = 'TicketAdminAttachment'
WHERE r.`role_key` = 'admin';
