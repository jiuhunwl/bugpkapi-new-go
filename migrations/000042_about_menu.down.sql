-- 回滚：移除「关于系统」菜单及角色授权。
DELETE FROM `bp_role_menu` WHERE `menu_id` = 178;
DELETE FROM `bp_menu` WHERE `id` = 178;
