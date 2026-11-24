ALTER TABLE `bp_interface_call_log`
  MODIFY COLUMN `upstream_url` varchar(1024) DEFAULT NULL COMMENT '上游节点引用（兼容历史结构）';
