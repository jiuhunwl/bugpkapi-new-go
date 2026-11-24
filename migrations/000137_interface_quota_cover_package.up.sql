-- 000137: 接口配额是否覆盖套餐用户（套餐豁免选项）。
--
-- 默认 0（豁免）：保留「套餐优先」——本次请求已由套餐额度承接时，
-- 整层接口配额不检查也不计数，套餐用户只受套餐日/总量额度约束。
-- 设为 1（不豁免）：接口配额对套餐用户同样生效（与用户组总量配额叠加）。
ALTER TABLE `bp_interface_quota`
  ADD COLUMN `cover_package` tinyint NOT NULL DEFAULT 0
  COMMENT '1 = 接口配额同样约束套餐用户；0 = 套餐优先，已由套餐承接的请求跳过本层' AFTER `count_mode`;
