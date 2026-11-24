package gateway

// 接口计量策略的三个维度（bp_interface_metering_policy / bp_interface_role_metering）。
//
// 拆维度的原因：bp_interface_api.billing_enabled 一个布尔既被当成「不扣余额/点数」，
// 又被当成「不扣次数」，而套餐次数它其实管不到。这里把三件事分开，各自可按接口、
// 可按用户组单独定：
//
//	asset_mode   资产扣减：inherit = 按 billing_enabled + 价格；free = 永不扣；charge = 有价格就必扣
//	package_mode 套餐次数：inherit = 有可用套餐就扣（1.2.88 现行为）；consume = 强制计；skip = 不占次数
//	group_mode   用户组免费额度：inherit = 按角色配额开关；count = 强制计；skip = 不计入窗口
//
// 所有判定都把空串和未知值按 inherit 处理，因此未配置策略的接口（包括存量单测里手工
// 构造的 InterfaceConfig）行为与 1.2.88 完全一致。
const (
	meteringInherit        = "inherit"
	meteringAssetFree      = "free"
	meteringAssetCharge    = "charge"
	meteringPackageConsume = "consume"
	meteringPackageSkip    = "skip"
	meteringGroupCount     = "count"
	meteringGroupSkip      = "skip"
)

// withMeteringFor 把该用户组的覆盖合并进接口级策略，返回解析后的副本。
// InterfaceConfig 全程按值传递，所以这里改动的是本次请求私有的拷贝，
// 不会污染网关的配置缓存。
func (c InterfaceConfig) withMeteringFor(roleID int64) InterfaceConfig {
	override, ok := c.RoleMetering[roleID]
	if !ok {
		return c
	}
	c.AssetMode = overrideOrDefault(c.AssetMode, override.AssetMode)
	c.PackageMode = overrideOrDefault(c.PackageMode, override.PackageMode)
	c.GroupMode = overrideOrDefault(c.GroupMode, override.GroupMode)
	return c
}

func overrideOrDefault(base, override string) string {
	if override == "" || override == meteringInherit {
		return base
	}
	return override
}

// hasPricedAsset 沿用现有的字面比较口径，不引入新的价格解析规则。
func (c InterfaceConfig) hasPricedAsset() bool {
	return c.Price != "0.0000" || c.PointPrice > 0
}

// assetsFree 报告本次调用是否不扣余额与点数。
func (c InterfaceConfig) assetsFree() bool {
	switch c.AssetMode {
	case meteringAssetFree:
		return true
	case meteringAssetCharge:
		// 强制扣费也变不出钱来：价格全为 0 时仍按零价接口处理。
		return !c.hasPricedAsset()
	default:
		return c.BillingDisabled || !c.hasPricedAsset()
	}
}

// bypassesBillingRules 报告本次调用是否跳过扣费条件判定：
// 显式强制扣费时不看条件；本来就不扣资产时条件也无意义。
func (c InterfaceConfig) bypassesBillingRules() bool {
	switch c.AssetMode {
	case meteringAssetCharge, meteringAssetFree:
		return true
	default:
		return c.BillingDisabled
	}
}

// consumesPackageQuota 报告本次调用是否占用套餐次数。consume 与 inherit 在接口级
// 等价（有可用套餐才扣），差别在角色覆盖：接口级 skip 时仍可让某个组按套餐计量。
func (c InterfaceConfig) consumesPackageQuota() bool {
	switch c.PackageMode {
	case meteringPackageSkip:
		return false
	case meteringPackageConsume:
		return true
	default:
		return true
	}
}

// countsGroupQuota 报告本次调用是否计入用户组免费额度窗口。
func (c InterfaceConfig) countsGroupQuota() bool {
	return c.GroupMode != meteringGroupSkip
}

// forcesGroupQuota 报告是否在角色关闭配额的情况下仍然计数。
func (c InterfaceConfig) forcesGroupQuota() bool {
	return c.GroupMode == meteringGroupCount
}
