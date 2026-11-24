ALTER TABLE `bp_interface_call_log`
  MODIFY COLUMN `upstream_url` TEXT DEFAULT NULL COMMENT '上游节点引用（不可逆脱敏）';
