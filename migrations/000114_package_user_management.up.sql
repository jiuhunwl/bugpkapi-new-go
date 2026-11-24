-- 用户套餐管理：管理员查看、启用/禁用、撤销单个用户的套餐权益。

-- 管理端：用户套餐页（挂在「套餐管理」目录下）
INSERT INTO `bp_menu`
  (`id`, `pid`, `name`, `path`, `component`, `redirect`, `type`, `auth_code`, `meta`, `status`)
VALUES
  (243, 70, 'UserPackageList', '/package/user', '/package/user/list', NULL, 'menu', 'package:user:list', JSON_OBJECT('title', 'system.package.userTitle', 'order', 5), 1)
ON DUPLICATE KEY UPDATE
  `pid` = VALUES(`pid`),
  `path` = VALUES(`path`),
  `component` = VALUES(`component`),
  `redirect` = VALUES(`redirect`),
  `type` = VALUES(`type`),
  `auth_code` = VALUES(`auth_code`),
  `meta` = VALUES(`meta`),
  `status` = VALUES(`status`);

-- 按钮权限：启用/禁用用户权益、撤销用户权益
INSERT INTO `bp_menu`
  (`id`, `pid`, `name`, `path`, `component`, `redirect`, `type`, `auth_code`, `meta`, `status`)
VALUES
  (244, 243, 'UserPackageUpdate', '', NULL, NULL, 'button', 'package:user:update', JSON_OBJECT('title', 'system.package.userUpdateTitle'), 1),
  (245, 243, 'UserPackageDelete', '', NULL, NULL, 'button', 'package:user:delete', JSON_OBJECT('title', 'system.package.userDeleteTitle'), 1)
ON DUPLICATE KEY UPDATE
  `pid` = VALUES(`pid`),
  `path` = VALUES(`path`),
  `component` = VALUES(`component`),
  `redirect` = VALUES(`redirect`),
  `type` = VALUES(`type`),
  `auth_code` = VALUES(`auth_code`),
  `meta` = VALUES(`meta`),
  `status` = VALUES(`status`);

INSERT IGNORE INTO `bp_role_menu` (`role_id`, `menu_id`)
SELECT 1, `id` FROM `bp_menu` WHERE `id` IN (243, 244, 245);
