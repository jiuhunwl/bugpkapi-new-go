-- 回滚 000121：首页滚动公告回到 bp_home_notice_config 单例配置。
-- 该表在整个升级期间未被改动，因此回滚后旧代码读到的仍是升级前的原文。

-- 只有仍与旧配置完全对应的迁移行才整条还原：标题、发布时间三重匹配说明管理员升级后
-- 没有改动过它，把它自己的译文送回 home_notice 资源键即可无损回滚。
INSERT INTO `bp_i18n_entry`
  (`resource_type`, `resource_key`, `field`, `locale`, `value`, `source_hash`, `updated_by`)
SELECT 'home_notice', '1', 'content', e.`locale`, e.`value`, e.`source_hash`, e.`updated_by`
FROM `bp_i18n_entry` e
JOIN `bp_notice` n ON n.`id` = CAST(e.`resource_key` AS UNSIGNED)
JOIN `bp_home_notice_config` o ON o.`id` = 1
WHERE e.`resource_type` = 'notice'
  AND e.`field` = 'title'
  AND n.`is_home_ticker` = 1
  AND n.`creator_id` IS NULL
  AND n.`title` = TRIM(o.`content`)
  AND n.`publish_time` = o.`updated_at`
  AND TRIM(o.`content`) <> ''
ON DUPLICATE KEY UPDATE
  `value` = VALUES(`value`),
  `source_hash` = VALUES(`source_hash`),
  `updated_by` = VALUES(`updated_by`);

DELETE e FROM `bp_i18n_entry` e
JOIN `bp_notice` n ON n.`id` = CAST(e.`resource_key` AS UNSIGNED)
JOIN `bp_home_notice_config` o ON o.`id` = 1
WHERE e.`resource_type` = 'notice'
  AND e.`field` = 'title'
  AND n.`is_home_ticker` = 1
  AND n.`creator_id` IS NULL
  AND n.`title` = TRIM(o.`content`)
  AND n.`publish_time` = o.`updated_at`
  AND TRIM(o.`content`) <> '';

DELETE n FROM `bp_notice` n
JOIN `bp_home_notice_config` o ON o.`id` = 1
WHERE n.`is_home_ticker` = 1
  AND n.`creator_id` IS NULL
  AND n.`title` = TRIM(o.`content`)
  AND n.`publish_time` = o.`updated_at`
  AND TRIM(o.`content`) <> '';

-- 管理员升级后编辑过的滚动公告（标题已改动，或本就由人工创建）保留为普通公告，
-- 只是随列删除一起失去投放位标记。

UPDATE `bp_menu`
SET `component` = '/notice/admin/list',
    `meta` = JSON_SET(
      COALESCE(`meta`, JSON_OBJECT()),
      '$.title', 'system.notice.listTitle',
      '$.order', 1
    )
WHERE `id` = 121;

UPDATE `bp_menu`
SET `component` = '/notice/admin/home-notice/index',
    `meta` = JSON_SET(
      COALESCE(`meta`, JSON_OBJECT()),
      '$.title', 'system.notice.homeConfigTitle',
      '$.order', 2
    )
WHERE `id` = 128;

UPDATE `bp_menu`
SET `meta` = JSON_REMOVE(`meta`, '$.icon')
WHERE `id` = 121 AND JSON_VALID(COALESCE(`meta`, '{}'));

UPDATE `bp_menu`
SET `meta` = JSON_REMOVE(`meta`, '$.hideInMenu', '$.navigationCleanup')
WHERE `id` = 128 AND JSON_VALID(COALESCE(`meta`, '{}'));

ALTER TABLE `bp_notice`
  DROP KEY `idx_bp_notice_ticker`,
  DROP COLUMN `expire_time`,
  DROP COLUMN `is_home_ticker`;

-- title 保留 varchar(300)：回滚后 1.2.70 的服务层仍按 100 字校验，列宽是惰性的；
-- 收窄反而会让升级期间已写入的长标题在严格模式下报 Data too long。
-- expire_time 一列随回滚消失属于预期：旧版本没有失效时间概念。
