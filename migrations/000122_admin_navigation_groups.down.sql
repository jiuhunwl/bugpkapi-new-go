-- 回滚 000122：顶级分组顺序、子页归属、账单记录组件指针与 16 行问号标题全部退回
-- 迁移前（即 000121 之后）的状态。问号原样写回是刻意的——回滚不该顺手改文案，
-- 否则 down 之后再 up 就不是同一份数据了。

-- ── 1. 子页归位 ────────────────────────────────────────────────────────────
UPDATE `bp_menu`
SET `pid` = CASE `id` WHEN 100 THEN 70 WHEN 31 THEN 30 WHEN 211 THEN 30 WHEN 160 THEN 30
                     WHEN 241 THEN 30 WHEN 221 THEN 220 WHEN 222 THEN 220 WHEN 223 THEN 220 END
WHERE `id` IN (100, 31, 211, 160, 241, 221, 222, 223);

-- 工单管理退回顶级（204 原本就是页面型菜单，无需还原 type/component）。
UPDATE `bp_menu`
SET `pid` = 0
WHERE `id` = 204 AND `name` = 'AdminTicketManagement';

DELETE FROM `bp_menu` WHERE `id` = 900100 AND `name` = 'TicketSupportHub';

UPDATE `bp_menu`
SET `redirect` = NULL,
    `meta` = JSON_REMOVE(
      COALESCE(`meta`, JSON_OBJECT()),
      '$.hideInMenu',
      '$.navigationCleanup'
    )
WHERE `id` = 220 AND `name` = 'RedeemCode';

UPDATE `bp_menu`
SET `meta` = JSON_REMOVE(COALESCE(`meta`, JSON_OBJECT()), '$.audience')
WHERE `id` = 241 AND `name` = 'SystemInboxBroadcast';

-- ── 2. 同级 order 还原 ─────────────────────────────────────────────────────
UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.order',
  CASE `id`
    WHEN 40 THEN 99995 WHEN 60 THEN 3 WHEN 70 THEN 4 WHEN 90 THEN 5
    WHEN 10 THEN 9996 WHEN 120 THEN 7 WHEN 30 THEN 9997 WHEN 204 THEN 6
    WHEN 84 THEN 3 WHEN 210 THEN 4 WHEN 203 THEN 5
    WHEN 41 THEN 1 WHEN 50 THEN 2
    WHEN 71 THEN 1 WHEN 75 THEN 2 WHEN 79 THEN 3 WHEN 243 THEN 5 WHEN 83 THEN 4
    WHEN 221 THEN 1 WHEN 222 THEN 2 WHEN 223 THEN 3
    WHEN 101 THEN 1 WHEN 103 THEN 2 WHEN 107 THEN 3
    WHEN 11 THEN 1 WHEN 20 THEN 2 WHEN 31 THEN 1
    WHEN 121 THEN 1 WHEN 130 THEN 3 WHEN 138 THEN 4 WHEN 181 THEN 7 WHEN 190 THEN 8
    WHEN 170 THEN 3 WHEN 211 THEN 10 WHEN 160 THEN 6 WHEN 241 THEN 9
    WHEN 150 THEN 2 WHEN 152 THEN 3 WHEN 155 THEN 4 WHEN 158 THEN 5
    WHEN 176 THEN 7 WHEN 179 THEN 9 WHEN 185 THEN 10
    WHEN 230 THEN 7 WHEN 232 THEN 8 WHEN 250 THEN 11
  END)
WHERE `id` IN (
  40, 60, 70, 90, 10, 120, 30, 204, 84, 210, 203,
  41, 50, 71, 75, 79, 243, 83, 221, 222, 223, 101, 103, 107,
  11, 20, 31, 121, 130, 138, 181, 190, 170, 211, 160, 241,
  150, 152, 155, 158, 176, 179, 185, 230, 232, 250
);

-- 密钥管理同时退回按受众分开的 order，否则回滚后管理端仍是 30。
UPDATE `bp_menu`
SET `meta` = JSON_SET(
  COALESCE(`meta`, JSON_OBJECT()),
  '$.adminOrder', 3,
  '$.userOrder', 3
)
WHERE `id` = 60 AND `name` = 'ApiKeyManagement';

-- ── 3. 分组标题与账单记录组件指针 ──────────────────────────────────────────
UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.title',
  CASE `id`
    WHEN 40 THEN 'system.interface.title' WHEN 10 THEN 'system.user.title'
    WHEN 100 THEN 'system.payment.title'
  END)
WHERE `id` IN (40, 10, 100);

UPDATE `bp_menu`
SET `component` = '/interface/log'
WHERE `id` = 83 AND `name` = 'BillingRecordList';

-- ── 4. 问号标题写回原值 ─────────────────────────────────────────────────────
UPDATE `bp_menu`
SET `meta` = JSON_SET(COALESCE(`meta`, JSON_OBJECT()), '$.title',
  CASE `id`
    WHEN 176 THEN 'IP ???'
    WHEN 177 THEN '??IP?????'
    WHEN 179 THEN '?????'
    WHEN 180 THEN '???????'
    WHEN 181 THEN '????'
    WHEN 182 THEN '????'
    WHEN 183 THEN '????'
    WHEN 184 THEN '????'
    WHEN 185 THEN '?????'
    WHEN 186 THEN '???????'
    WHEN 187 THEN '???????'
    WHEN 188 THEN '??????'
    WHEN 189 THEN '??????'
    WHEN 190 THEN '????'
    WHEN 191 THEN '??????'
    WHEN 192 THEN '???????'
  END)
WHERE `id` IN (176, 177, 179, 180, 181, 182, 183, 184, 185, 186, 187, 188, 189, 190, 191, 192);
