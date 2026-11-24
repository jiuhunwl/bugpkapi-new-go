-- 000139: 降级目标支持自定义内部地址。
--
-- 与接口轮询的上游节点一致，降级目标可以不是已注册的公开接口，而是
-- 管理员直接填写的 HTTP(S) 内部地址。默认目标与用户组覆盖都支持：
--   * 填写 target_interface_id => 使用已注册接口的执行配置；
--   * 填写 target_url          => 使用当前接口的清洗/代理/超时配置，仅替换上游地址；
--   * 两者只能二选一，留空表示跟随默认目标。
ALTER TABLE `bp_interface_degrade_policy`
  ADD COLUMN `default_target_url` varchar(1024) DEFAULT NULL
  COMMENT 'Custom internal target URL; mutually exclusive with default_target_interface_id'
  AFTER `default_target_interface_id`;

ALTER TABLE `bp_interface_role_degrade`
  ADD COLUMN `target_url` varchar(1024) DEFAULT NULL
  COMMENT 'Custom internal target URL; NULL = inherit default target'
  AFTER `target_interface_id`;
