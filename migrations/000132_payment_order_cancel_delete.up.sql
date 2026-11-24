-- 000132: 交易记录支持「取消订单」与「删除订单」。
--
-- deleted_at：用户与管理员的删除都只是软隐藏。订单行保留，支付审计与旧支付载荷不丢，
-- 迟到的到账通知仍能补结算；只有列表查询过滤 deleted_at，按订单号查询与结算路径不过滤。
--
-- CANCELLED 此前只是只读枚举，没有写入方；现在由用户主动取消写入。它与 EXPIRED 同类，
-- 代表「本地不再允许发起支付」而不是渠道关单（渠道关单是 CLOSED），因此随后渠道确认
-- 到账时依然允许结算并把已释放的限量套餐占用补回。
ALTER TABLE `bp_payment_order`
  ADD COLUMN `deleted_at` datetime DEFAULT NULL COMMENT '交易记录删除（软隐藏）时间' AFTER `settled_at`;

-- 管理端「删除订单」按钮权限，挂在 107 支付订单列表下。
-- 菜单 ID 取 900112：当前最大种子 ID 为 900111（登录态配置按钮）。
INSERT INTO `bp_menu`
  (`id`, `pid`, `name`, `path`, `component`, `redirect`, `type`, `auth_code`, `meta`, `status`)
VALUES
  (900112, 107, 'PaymentOrderDelete', '', NULL, NULL, 'button', 'payment:order:delete',
   JSON_OBJECT('title', 'Delete Payment Order'), 1)
ON DUPLICATE KEY UPDATE
  `pid` = VALUES(`pid`),
  `type` = VALUES(`type`),
  `auth_code` = VALUES(`auth_code`),
  `meta` = VALUES(`meta`),
  `status` = VALUES(`status`);

INSERT IGNORE INTO `bp_role_menu` (`role_id`, `menu_id`)
SELECT 1, id FROM `bp_menu` WHERE id = 900112;
