-- 000140: 接口配额的用户组覆盖新增 ignore 模式。
--
-- 与既有 unlimited 的区别：
--   unlimited = 只放开「单用户上限」，请求仍会检查并消耗全平台总量计数；
--   ignore    = 整层跳过：不检查、不计数，连全平台总量都不占。
--
-- 典型用途：内部账号 / 赞助 / 白名单用户组的流量不占用对外售卖的全平台额度池。
-- 需要注意：ignore 的请求完全不受本层约束（QPS、用户组总量配额、首日额度仍然生效），
-- 因此上游实际承受的流量会高于 all_users_* 配置值，属于管理端显式选择的结果。
ALTER TABLE `bp_interface_role_quota` DROP CHECK `chk_bp_interface_role_quota_mode`;
ALTER TABLE `bp_interface_role_quota`
  ADD CONSTRAINT `chk_bp_interface_role_quota_mode`
  CHECK (`mode` IN ('inherit', 'unlimited', 'adjust', 'override', 'ignore'));
