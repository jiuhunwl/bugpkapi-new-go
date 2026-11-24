-- 000147: 用户端货币展示名称。
--
-- 只影响用户端「货币称呼」：把 ¥/人民币 的文案替换为运营自定义名称
-- （如积分、赞助额度）。余额、流水、订单金额仍按人民币基数存储，
-- 汇率换算（usd_cny_rate）照旧只服务于真实货币结算，本字段不参与任何换算。
ALTER TABLE `bp_payment_currency_config`
  ADD COLUMN `display_name` varchar(32) NOT NULL DEFAULT ''
    COMMENT '用户端货币展示名称；空=按结算货币显示' AFTER `goods_name`;
