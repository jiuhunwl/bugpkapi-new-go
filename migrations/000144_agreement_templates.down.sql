-- 撤销协议模板模块（与 000143_agreement_templates.up.sql 对应）。
DELETE FROM `bp_role_menu` WHERE `menu_id` BETWEEN 900113 AND 900114;
DELETE FROM `bp_menu` WHERE `id` BETWEEN 900113 AND 900114;
DROP TABLE IF EXISTS `bp_agreement_template`;
DROP TABLE IF EXISTS `bp_agreement_config`;
