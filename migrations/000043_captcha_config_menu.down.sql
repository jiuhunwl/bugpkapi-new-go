-- 撤销「验证码配置」菜单种子（与 000043_captcha_config_menu.up.sql 对应）。
DELETE FROM `bp_role_menu` WHERE `menu_id` BETWEEN 179 AND 180 AND `role_id` = 1;
DELETE FROM `bp_menu` WHERE `id` BETWEEN 179 AND 180;
