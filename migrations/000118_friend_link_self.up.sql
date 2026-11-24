-- 友链自助管理的数据基础。
--
-- 1) bp_friend_link 此前只存 applicant_name/applicant_email 文本，没有归属账号，
--    所以「用户管自己的友链」无从查起。审核通过时已经写入了
--    source_application_id，历史数据据此回填 user_id；管理员手工新增的友链
--    保持 user_id=0（不属于任何用户）。
ALTER TABLE `bp_friend_link`
  ADD COLUMN `user_id` bigint NOT NULL DEFAULT 0 COMMENT 'Owning account id, 0 = added by admin' AFTER `applicant_email`,
  ADD KEY `idx_bp_friend_link_user` (`user_id`, `status`, `id`);

UPDATE `bp_friend_link` l
JOIN `bp_friend_link_application` a ON a.`id` = l.`source_application_id`
SET l.`user_id` = a.`user_id`
WHERE l.`user_id` = 0 AND a.`user_id` > 0;

-- 2) 用户改已上线的友链不能直接生效，走同一张申请表的变更请求：
--    NEW=首次申请（既有链路），EDIT=修改信息，REMOVE=申请下架。
--    审核通过时才把请求里的新值落到 bp_friend_link，因此审核列表、驳回模板、
--    邮件通知与审计全部复用现有链路，不再新增一套并行的审核状态。
ALTER TABLE `bp_friend_link_application`
  ADD COLUMN `link_id` bigint NOT NULL DEFAULT 0 COMMENT 'Target live link id for EDIT/REMOVE' AFTER `user_id`,
  ADD COLUMN `change_kind` varchar(16) NOT NULL DEFAULT 'NEW' COMMENT 'NEW/EDIT/REMOVE' AFTER `link_id`,
  ADD KEY `idx_bp_friend_link_app_link` (`link_id`, `status`);
