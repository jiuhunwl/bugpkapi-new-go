DELETE rm
FROM `bp_role_menu` rm
JOIN `bp_menu` m ON m.`id` = rm.`menu_id`
WHERE m.`name` = 'TicketAdminAttachment';

DELETE FROM `bp_menu`
WHERE `name` = 'TicketAdminAttachment';
