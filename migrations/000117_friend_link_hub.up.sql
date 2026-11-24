-- 友链三合一：「友情链接」「友链中心」「友链配置」原来是三个菜单入口，其中
-- 友链配置只是把友链中心重渲染一遍。这里把 138 升级为唯一可见入口（三个页签：
-- 申请审核 / 友链列表 / 站点配置），133 与 140 保留为隐藏的别名路由。
--
-- 不删除 133/140 的行：菜单权限码来自 bp_menu.auth_code 与角色授权，删除会让
-- 已被单独授权「友情链接」或「友链配置」的角色直接丢失 friend-link:list /
-- friend-link:config:update，页面按钮随之失效。

UPDATE `bp_menu`
SET `name` = 'FriendLinkCenter',
    `path` = '/content/friend-link',
    `component` = '/friend-link',
    `redirect` = NULL,
    `type` = 'menu',
    `auth_code` = 'friend-link:application:list',
    `meta` = JSON_SET(
      COALESCE(`meta`, JSON_OBJECT()),
      '$.title', 'system.friendLink.applicationTitle',
      '$.icon', 'mdi:link-variant',
      '$.order', 4
    ),
    `status` = 1
WHERE `id` = 138 AND `name` = 'FriendLinkApplications';

UPDATE `bp_menu`
SET `component` = '/friend-link',
    `meta` = JSON_SET(
      COALESCE(`meta`, JSON_OBJECT()),
      '$.hideInMenu', true,
      '$.navigationCleanup', '000117-friend-link-hub'
    ),
    `status` = 1
WHERE `id` IN (133, 140);

-- 能看到任意一个友链入口的角色，都能看到合并后的友链中心。
INSERT IGNORE INTO `bp_role_menu` (`role_id`, `menu_id`)
SELECT `role_id`, 138
FROM `bp_role_menu`
WHERE `menu_id` IN (133, 140);
