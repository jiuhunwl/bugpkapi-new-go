-- 回滚只把菜单重新拆开：133/140 恢复可见，138 恢复原名原路径。
-- 组件仍指向合并后的友链中心（config.vue 与 applications.vue 已删除），
-- 因为把菜单指回不存在的页面组件会让路由退化成 404 兜底页。
UPDATE `bp_menu`
SET `meta` = JSON_REMOVE(
      COALESCE(`meta`, JSON_OBJECT()),
      '$.hideInMenu',
      '$.navigationCleanup'
    )
WHERE `id` IN (133, 140);

UPDATE `bp_menu`
SET `name` = 'FriendLinkApplications',
    `path` = '/content/friend-link-applications',
    `meta` = JSON_SET(
      COALESCE(`meta`, JSON_OBJECT()),
      '$.title', 'system.friendLink.applicationTitle',
      '$.order', 5
    )
WHERE `id` = 138 AND `name` = 'FriendLinkCenter';
