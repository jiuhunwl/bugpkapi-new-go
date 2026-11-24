ALTER TABLE `bp_user`
  ADD COLUMN `ban_permanent` tinyint NOT NULL DEFAULT 0
    COMMENT 'Whether the current account ban is permanent'
    AFTER `status`,
  ADD COLUMN `ban_until` datetime NULL
    COMMENT 'Temporary ban expiry; null for active or permanently banned accounts'
    AFTER `ban_permanent`,
  ADD COLUMN `ban_reason` varchar(500) NOT NULL DEFAULT ''
    COMMENT 'Administrator-provided account ban reason'
    AFTER `ban_until`,
  ADD KEY `idx_bp_user_ban_state` (`status`, `ban_permanent`, `ban_until`);

-- Preserve installations that used status=0 before timed bans existed. Those
-- accounts remain unavailable until an administrator explicitly unbans them.
UPDATE `bp_user`
SET `ban_permanent` = 1,
    `ban_reason` = CASE
      WHEN TRIM(`ban_reason`) = '' THEN 'Legacy disabled account'
      ELSE `ban_reason`
    END
WHERE `status` = 0;
