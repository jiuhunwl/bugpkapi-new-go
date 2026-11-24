-- 账号封禁 = 全量封禁：封禁时把名下启用中的接口 Key 一并冻结。
--
-- 为什么在 Key 行上再落一个状态，而不是只靠 bp_user.status 判定：
--   1) 网关只判 user.status 时，Key 列表仍显示「启用」，运营无法从数据上确认
--      该账号的调用凭证已失效；两处状态不一致正是上一轮「封禁了但 key 还能用」
--      的观感来源。
--   2) 任何新增的鉴权入口（未来的平面、离线导出、按 Key 维度的白名单下发）
--      只要读了 bp_api_key 就可能漏判 user.status；把暂停落到 Key 行是纵深防御。
--   3) 解封需要精确恢复「因封禁而停」的 Key，不能连带把用户自己主动禁用的
--      Key 也打开，所以必须有独立的标记位。
--
-- suspended_by_ban 语义：
--   0 = 未因封禁暂停（正常行，含用户主动禁用）
--   1 = 因临时封禁暂停；解封时自动恢复为启用
--   2 = 因永久封禁作废；解封后仍保持禁用，需管理员在 Key 管理里手动启用
ALTER TABLE `bp_api_key`
  ADD COLUMN `suspended_by_ban` tinyint NOT NULL DEFAULT 0
    COMMENT '封禁联动：0=未暂停 1=临时封禁暂停(解封恢复) 2=永久封禁作废(不自动恢复)'
    AFTER `status`;

-- 历史数据对齐：本次变更之前封禁的账号，其 Key 仍是 status=1。
-- 只处理「当前确实处于封禁态」的账号，判断式与 accountstatus.EffectiveStatusSQL
-- 完全一致：status=0 且不是「临时封禁已到期」。已到期的临时封禁账号即将由
-- 到期解封任务恢复，不能在这里被误冻结。
-- 永久封禁记 2（解封后也不自动恢复），临时封禁记 1（解封即恢复）。
UPDATE `bp_api_key` k
  INNER JOIN `bp_user` u ON u.id = k.user_id
   SET k.status = 0,
       k.suspended_by_ban = IF(u.ban_permanent = 1, 2, 1)
 WHERE u.status = 0
   AND NOT (u.ban_permanent = 0
            AND u.ban_until IS NOT NULL
            AND u.ban_until <= NOW())
   AND k.status = 1;
