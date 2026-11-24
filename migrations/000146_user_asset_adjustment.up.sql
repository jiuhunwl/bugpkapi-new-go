-- 000146: 用户资产增量调整（余额/点数增减）与资产变动记录。
--
-- 调整接口对 bp_user 行加锁后写入 bp_wallet_transaction(biz_type=ADMIN_BALANCE_ADJUST)，
-- 与充值结算的 balance=balance+? 串行化，避免管理员改余额时覆盖并发充值。
--
-- 管理端「资产变动记录」按 biz_type + created_at 分页读取收入/购买/调整类流水，
-- 新索引让该列表无需扫描按次调用预扣产生的大量 OPEN_CALL_* 行。
ALTER TABLE `bp_wallet_transaction`
  ADD KEY `idx_bp_wallet_transaction_biz_created` (`biz_type`, `created_at`);

-- 「余额调整」按钮权限，挂在 11 系统用户列表下。
-- 菜单 ID 取 900123：当前最大种子 ID 为 900122（图床配置按钮）。
INSERT INTO `bp_menu`
  (`id`, `pid`, `name`, `path`, `component`, `redirect`, `type`, `auth_code`, `meta`, `status`)
VALUES
  (900123, 11, 'SystemUserAssetAdjust', '', NULL, NULL, 'button', 'system:user:balance-adjust',
   JSON_OBJECT('title', 'Adjust user balance', 'order', 5), 1)
ON DUPLICATE KEY UPDATE
  `pid` = VALUES(`pid`),
  `type` = VALUES(`type`),
  `auth_code` = VALUES(`auth_code`),
  `meta` = VALUES(`meta`),
  `status` = VALUES(`status`);

INSERT IGNORE INTO `bp_role_menu` (`role_id`, `menu_id`)
VALUES (1, 900123);
