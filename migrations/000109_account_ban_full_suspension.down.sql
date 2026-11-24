-- 回滚前把因封禁暂停的 Key 恢复为启用，避免降级到旧版本后这些账号的
-- 调用凭证被永久锁死（旧版本不认识 suspended_by_ban，但网关仍判 user.status）。
UPDATE `bp_api_key` k
  INNER JOIN `bp_user` u ON u.id = k.user_id
   SET k.status = 1,
       k.suspended_by_ban = 0
 WHERE k.suspended_by_ban = 1
   AND u.status = 1;

ALTER TABLE `bp_api_key` DROP COLUMN `suspended_by_ban`;
