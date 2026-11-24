-- Interface-local billing switches. Existing behavior is preserved by DEFAULT 1.
ALTER TABLE bp_interface_api
  ADD COLUMN billing_enabled tinyint NOT NULL DEFAULT 1 COMMENT '接口计费总开关；0 不扣费且保留价格和规则',
  ADD COLUMN billing_log_enabled tinyint NOT NULL DEFAULT 1 COMMENT '是否展示新增计费日志并保留详细计费快照';

-- Keep the financial ledger for settlement/refund/reconciliation even when
-- optional billing logs are disabled. Capture visibility at invocation time.
ALTER TABLE bp_billing_record
  ADD COLUMN log_visible tinyint NOT NULL DEFAULT 1 COMMENT '计费日志可见性；不影响内部结算流水';
