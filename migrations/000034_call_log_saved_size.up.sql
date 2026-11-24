ALTER TABLE `bp_interface_call_log`
  ADD COLUMN `response_saved_size` bigint NOT NULL DEFAULT 0 COMMENT 'persisted response body size after masking and truncation' AFTER `response_size`;

UPDATE `bp_interface_call_log`
SET `response_saved_size` = OCTET_LENGTH(COALESCE(`response_body`, ''));
