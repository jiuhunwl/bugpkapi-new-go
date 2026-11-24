ALTER TABLE `bp_interface_call_log`
  MODIFY COLUMN `response_size` bigint NOT NULL DEFAULT 0 COMMENT 'response body saved size bytes';
