ALTER TABLE `bp_interface_call_log`
  ADD COLUMN `response_truncated` tinyint NOT NULL DEFAULT 0 COMMENT 'response body was truncated or omitted before persistence' AFTER `response_size`;

UPDATE `bp_interface_call_log`
SET `response_truncated` = 1
WHERE `response_body` = '[BINARY_RESPONSE_OMITTED]'
   OR (`response_size` > 0 AND `response_size` > OCTET_LENGTH(COALESCE(`response_body`, '')));
