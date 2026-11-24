-- 公告三合一：「公告管理」「首页滚动公告」「弹窗公告」原来分属两套数据、两个菜单、
-- 两个 API 模块。首页滚动公告是一张独立的单例配置表（bp_home_notice_config），
-- 只能放 300 字纯文本，既不参与公告中心列表，也不能置顶、不能设失效时间。
--
-- 这里把首页滚动位降级成公告条目自身的一个投放位（is_home_ticker），让所有对外
-- 文案共用同一条生命周期：一份正文、一套权限、一个生效/失效窗口。
--
-- bp_home_notice_config 保留结构与数据（本迁移不改动它），回滚到 1.2.70 及更早
-- 版本后旧代码仍可原样读取。

ALTER TABLE `bp_notice`
  ADD COLUMN `is_home_ticker` tinyint NOT NULL DEFAULT 0
    COMMENT '是否投放到首页滚动公告条' AFTER `is_popup`,
  ADD COLUMN `expire_time` datetime DEFAULT NULL
    COMMENT '失效时间，NULL=不自动失效' AFTER `publish_time`,
  ADD KEY `idx_bp_notice_ticker` (`status`, `is_home_ticker`, `publish_time`, `id`);

-- 标题同时充当首页滚动文案，所以放宽到原配置表的 300 字，
-- 否则下面迁入的长文案会被静默截断。
ALTER TABLE `bp_notice`
  MODIFY COLUMN `title` varchar(300) NOT NULL COMMENT '公告标题；首页滚动位取标题原文';

-- 把当前生效的首页滚动文案迁成一条公告。creator_id 留空作为「本迁移生成的行」的
-- 标记，down 迁移与后续清理都据此判断，不会误删管理员自己建的滚动公告。
INSERT INTO `bp_notice`
  (`title`, `summary`, `content_html`, `is_top`, `is_popup`, `is_home_ticker`,
   `status`, `publish_time`, `creator_id`, `updater_id`)
SELECT
  TRIM(o.`content`),
  LEFT(REPLACE(REPLACE(REPLACE(TRIM(o.`content`), '&', '&amp;'), '<', '&lt;'), '>', '&gt;'), 255),
  CONCAT('<p>', REPLACE(REPLACE(REPLACE(TRIM(o.`content`), '&', '&amp;'), '<', '&lt;'), '>', '&gt;'), '</p>'),
  1, 0, 1, 1, o.`updated_at`, NULL, NULL
FROM `bp_home_notice_config` o
WHERE o.`id` = 1
  AND o.`enabled` = 1
  AND TRIM(o.`content`) <> ''
  AND NOT EXISTS (SELECT 1 FROM `bp_notice` t WHERE t.`is_home_ticker` = 1);

-- 滚动文案的译文跟着走：home_notice.content -> 新公告的 title。
-- 关联键用「迁移行的标记 + 发布时间等于配置更新时间 + 标题等于原文」三重锁定，
-- 避开 golang-migrate 逐句执行时 LAST_INSERT_ID() 可能落到不同连接的问题。
INSERT INTO `bp_i18n_entry`
  (`resource_type`, `resource_key`, `field`, `locale`, `value`, `source_hash`, `updated_by`)
SELECT 'notice', CAST(n.`id` AS CHAR), 'title', e.`locale`, e.`value`, e.`source_hash`, e.`updated_by`
FROM `bp_i18n_entry` e
JOIN `bp_home_notice_config` o ON o.`id` = 1
JOIN `bp_notice` n
  ON n.`is_home_ticker` = 1
 AND n.`creator_id` IS NULL
 AND n.`publish_time` = o.`updated_at`
 AND n.`title` = TRIM(o.`content`)
WHERE e.`resource_type` = 'home_notice'
  AND e.`resource_key` = '1'
  AND e.`field` = 'content'
  AND TRIM(o.`content`) <> ''
ON DUPLICATE KEY UPDATE
  `value` = VALUES(`value`),
  `source_hash` = VALUES(`source_hash`),
  `updated_by` = VALUES(`updated_by`);

DELETE FROM `bp_i18n_entry` WHERE `resource_type` = 'home_notice';

-- 121 升为唯一可见入口（公告中心），128 保留成同组件的隐藏别名：
-- 不删行、不改 auth_code，已被单独授权 notice:home-config 的角色照旧能进滚动位页签，
-- 也不会因为多授予 notice:list 而看到以前看不到的公告列表。
UPDATE `bp_menu`
SET `component` = '/notice/admin/hub',
    `meta` = JSON_SET(
      COALESCE(`meta`, JSON_OBJECT()),
      '$.title', 'system.notice.hubTitle',
      '$.icon', 'mdi:bullhorn-outline',
      '$.order', 1
    ),
    `status` = 1
WHERE `id` = 121;

UPDATE `bp_menu`
SET `component` = '/notice/admin/hub',
    `meta` = JSON_SET(
      COALESCE(`meta`, JSON_OBJECT()),
      '$.hideInMenu', true,
      '$.navigationCleanup', '000121-notice-hub'
    ),
    `status` = 1
WHERE `id` = 128;
