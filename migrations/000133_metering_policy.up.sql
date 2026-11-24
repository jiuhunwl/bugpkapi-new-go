-- 接口计量策略：把「扣不扣」拆成三个正交维度（2026-11-05）。
--
-- 现状是 bp_interface_api.billing_enabled 一个布尔试图同时管「资产扣减」和
-- 「次数计量」，而套餐次数它根本管不到。这里把三个维度拆开：
--
--   asset_mode   —— 余额 / 点数：inherit = 按 price、point_price 和
--                   billing_enabled 决定（即 1.2.88 现有行为）；
--                   free = 永不扣资产（保留价格配置，重开即恢复）；
--                   charge = 只要接口有价格就必扣，并跳过「扣费条件」判定；
--                            价格与点数全为 0 时无从扣起，仍按零价接口处理。
--   package_mode —— 套餐次数（bp_user_package_*.{day_used,total_used}）：
--                   inherit = 有可用套餐就扣（现有行为）；
--                   consume = 强制计入套餐；
--                   skip = 不占套餐次数。
--   group_mode   —— 用户组免费额度窗口（分/时/日/月）：
--                   inherit = 按 bp_role 的配额开关与窗口计数（现有行为）；
--                   count = 即使角色关闭了配额也计数；
--                   skip = 不计入免费额度窗口。
--
-- 三列默认全部 inherit，因此本迁移落库后线上行为零变化；需要破例的接口再逐条配置。
--
-- 兜底闸门不受本策略约束，代码里写死不可豁免：
--   * 注册后 24h 的首日额度（防开新号白嫖）；
--   * bp_interface_quota 的全平台总量 all_users_*。
-- 保存时若某条解析后的策略路径为「资产 free + 套餐 skip + 免费额度 skip」，
-- 必须已启用接口配额并设置至少一项全平台总量，否则拒绝保存。
CREATE TABLE IF NOT EXISTS `bp_interface_metering_policy` (
  `interface_id` bigint NOT NULL COMMENT '接口编号',
  `asset_mode` varchar(16) NOT NULL DEFAULT 'inherit' COMMENT '资产扣减：inherit/free/charge',
  `package_mode` varchar(16) NOT NULL DEFAULT 'inherit' COMMENT '套餐次数：inherit/consume/skip',
  `group_mode` varchar(16) NOT NULL DEFAULT 'inherit' COMMENT '用户组免费额度：inherit/count/skip',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (`interface_id`),
  CONSTRAINT `chk_bp_interface_metering_asset_mode`
    CHECK (`asset_mode` IN ('inherit', 'free', 'charge')),
  CONSTRAINT `chk_bp_interface_metering_package_mode`
    CHECK (`package_mode` IN ('inherit', 'consume', 'skip')),
  CONSTRAINT `chk_bp_interface_metering_group_mode`
    CHECK (`group_mode` IN ('inherit', 'count', 'skip'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='接口计量策略（资产/套餐/用户组免费额度三维）';

-- 用户组对三维策略的覆盖，与 bp_interface_role_quota 同一套解析思路：
--   inherit = 沿用本接口的策略值；其余取值 = 直接指定该维度的行为。
-- 只有存在非 inherit 维度时才落一行，缺行等价于三维全 inherit。
CREATE TABLE IF NOT EXISTS `bp_interface_role_metering` (
  `interface_id` bigint NOT NULL COMMENT '接口编号',
  `role_id` bigint NOT NULL COMMENT '用户组编号',
  `asset_mode` varchar(16) NOT NULL DEFAULT 'inherit' COMMENT '资产扣减：inherit/free/charge',
  `package_mode` varchar(16) NOT NULL DEFAULT 'inherit' COMMENT '套餐次数：inherit/consume/skip',
  `group_mode` varchar(16) NOT NULL DEFAULT 'inherit' COMMENT '用户组免费额度：inherit/count/skip',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (`interface_id`, `role_id`),
  KEY `idx_bp_interface_role_metering_role` (`role_id`),
  CONSTRAINT `chk_bp_interface_role_metering_asset_mode`
    CHECK (`asset_mode` IN ('inherit', 'free', 'charge')),
  CONSTRAINT `chk_bp_interface_role_metering_package_mode`
    CHECK (`package_mode` IN ('inherit', 'consume', 'skip')),
  CONSTRAINT `chk_bp_interface_role_metering_group_mode`
    CHECK (`group_mode` IN ('inherit', 'count', 'skip'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
  COMMENT='按用户组覆盖接口计量策略';
