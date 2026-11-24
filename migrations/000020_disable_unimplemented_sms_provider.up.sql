UPDATE `bp_auth_register_config`
SET `mobile_register_enabled` = 0
WHERE `id` = 1;

UPDATE `bp_message_provider_config`
SET `enabled` = 0
WHERE `channel` = 'sms' AND `provider_key` = 'aliyun';

UPDATE `bp_menu`
SET `status` = 0
WHERE `id` IN (155, 156, 157);
