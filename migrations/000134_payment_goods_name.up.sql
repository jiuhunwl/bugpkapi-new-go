-- 站点级「支付商品名」：微信/支付宝收银台与虎皮椒 title 统一用的中性名称（2026-11-05）。
--
-- 现状是商品名直接取订单的 subject（「余额充值-50元」「接口套餐-X-Y」这类明细名），
-- 它同时是账单一、订单记录和到账通知里的名称。虎皮椒走微信通道时，「充值」「点数」
-- 「套餐」这类虚拟商品字样容易被风控判定，导致支付受限或掉单。
--
-- 这里把「发给渠道的商品名」与「订单明细名」拆开：
--   * goods_name 非空 → 下单请求里的 title/description/subject 一律用它；
--   * goods_name 为空 → 沿用订单明细名（即历史行为，零变化）。
-- bp_payment_order.subject 始终保留明细名，对账、通知与用户订单列表不受影响。
--
-- 挂在 bp_payment_currency_config 这张站点级单例表上：同一份代码部署国内站/国际站时，
-- 各自后台独立取值，改完即时生效，不需要发版。
-- 虎皮椒官方限制 title ≤ 127 字节，因此保存时按字节校验，超长直接拒绝。
ALTER TABLE `bp_payment_currency_config`
  ADD COLUMN `goods_name` varchar(128) NOT NULL DEFAULT '' COMMENT '发给支付渠道的商品名；留空则沿用订单明细名' AFTER `usd_cny_rate`;

-- 默认给国内版一个风控友好的中性名称；已有部署首次跑本迁移即生效，
-- 后台改成空串可退回按明细名展示。
UPDATE `bp_payment_currency_config`
SET `goods_name` = 'API 开放平台技术服务费'
WHERE `id` = 1 AND `goods_name` = '';
