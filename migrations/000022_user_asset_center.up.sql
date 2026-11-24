INSERT INTO `bp_menu`
  (`id`, `pid`, `name`, `path`, `component`, `redirect`, `type`, `auth_code`, `meta`, `status`)
VALUES
  (174, 84, 'UserAssetCenter', '/purchase/assets', '/purchase/assets/index', NULL, 'menu', 'package:mall:list', JSON_OBJECT('title', 'system.package.assets.title', 'icon', 'lucide:wallet-cards', 'order', 5), 1)
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
SELECT id, 174 FROM `bp_role` WHERE `role_key` IN ('admin', 'user');
