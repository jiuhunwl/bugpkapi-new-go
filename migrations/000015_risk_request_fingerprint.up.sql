ALTER TABLE `bp_interface_call_log`
  ADD COLUMN `request_fingerprint` char(64) NOT NULL DEFAULT '' COMMENT 'HMAC request fingerprint used for privacy-safe risk grouping' AFTER `request_value`;

UPDATE `bp_interface_call_log`
SET `request_fingerprint` = SHA2(CONCAT(
  'legacy-masked-v1:',
  COALESCE(NULLIF(`request_params`, ''), NULLIF(`request_value`, ''), '')
), 256)
WHERE `charge_type` <> 'ADMIN'
  AND `request_fingerprint` = '';
