package packagepkg

type PageResult[T any] struct {
	Items []T   `json:"items"`
	Total int64 `json:"total"`
}

type Principal struct {
	ID           int64
	Username     string
	Roles        []string
	TokenVersion int
}

// PackageViewer 是套餐商城的浏览者身份：RoleIDs 即用户所属用户组。
// nil 表示管理端视角，不做用户组过滤与购买资格判定。
type PackageViewer struct {
	RoleIDs []int64
}

func (v *PackageViewer) HasRole(roleID int64) bool {
	if v == nil {
		return false
	}
	for _, id := range v.RoleIDs {
		if id == roleID {
			return true
		}
	}
	return false
}

// Allows 判断浏览者是否属于套餐允许的用户组；未启用限制时恒为 true。
func (v *PackageViewer) Allows(restricted int, roleIDs []int64) bool {
	if restricted == 0 || v == nil {
		return true
	}
	for _, roleID := range roleIDs {
		if v.HasRole(roleID) {
			return true
		}
	}
	return false
}

type ListParams struct {
	Keyword     string
	Page        int
	PageSize    int
	Status      *int
	InterfaceID int64
}

type GlobalPackage struct {
	CanPurchase bool   `json:"canPurchase"`
	CreateTime  string `json:"createTime"`
	DailyLimit  int64  `json:"dailyLimit"`
	// TotalLimit 为套餐生命周期总调用量上限，0 = 不限。
	TotalLimit      int64    `json:"totalLimit"`
	CountMode       string   `json:"countMode"`
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	PerUserLimit    int      `json:"perUserLimit"`
	Price           string   `json:"price"`
	QPSLimit        int      `json:"qpsLimit"`
	Remark          string   `json:"remark,omitempty"`
	RoleIDs         []int64  `json:"roleIds"`
	RoleNames       []string `json:"roleNames"`
	RoleRestricted  int      `json:"roleRestricted"`
	RoleVisibleOnly int      `json:"roleVisibleOnly"`
	// Hidden = 1 时商城列表过滤且不可购买，仅管理端可见、可下发。
	Hidden      int    `json:"hidden"`
	SaleEndAt   string `json:"saleEndAt,omitempty"`
	SaleStartAt string `json:"saleStartAt,omitempty"`
	Status      int    `json:"status"`
	StockLimit  int    `json:"stockLimit"`
	StockUsed   int    `json:"stockUsed"`
	UpdateTime  string `json:"updateTime"`
	ValidDays   int    `json:"validDays"`
	// RenewalEnabled=1 时，用户在有效期内再次购买本套餐会顺延已有权益的到期时间；
	// 0 则每次购买都新建一条独立权益。体验档、有总限量的套餐建议关掉。
	RenewalEnabled int `json:"renewalEnabled"`
}

type SaveGlobalPackageRequest struct {
	DailyLimit      int64   `json:"dailyLimit"`
	TotalLimit      int64   `json:"totalLimit"`
	CountMode       string  `json:"countMode"`
	Name            string  `json:"name"`
	PerUserLimit    int     `json:"perUserLimit"`
	Price           any     `json:"price"`
	QPSLimit        int     `json:"qpsLimit"`
	Remark          string  `json:"remark"`
	RoleIDs         []int64 `json:"roleIds"`
	RoleRestricted  int     `json:"roleRestricted"`
	RoleVisibleOnly int     `json:"roleVisibleOnly"`
	Hidden          *int    `json:"hidden"`
	SaleEndAt       string  `json:"saleEndAt"`
	SaleStartAt     string  `json:"saleStartAt"`
	Status          *int    `json:"status"`
	StockLimit      int     `json:"stockLimit"`
	ValidDays       int     `json:"validDays"`
	// 续费开关：nil = 保持开启（与列默认值一致），显式传 0 才关闭顺延。
	RenewalEnabled *int `json:"renewalEnabled"`
}

type InterfacePackage struct {
	APICode         string `json:"apiCode,omitempty"`
	AvatarURL       string `json:"avatarUrl,omitempty"`
	CanPurchase     bool   `json:"canPurchase"`
	CreateTime      string `json:"createTime"`
	ID              string `json:"id"`
	InterfaceID     string `json:"interfaceId"`
	InterfaceName   string `json:"interfaceName,omitempty"`
	InterfaceRemark string `json:"interfaceRemark,omitempty"`
	MinPrice        string `json:"minPrice,omitempty"`
	Name            string `json:"name"`
	Remark          string `json:"remark,omitempty"`
	RequestMethod   string `json:"requestMethod,omitempty"`
	// Hidden 为套餐级开关：1 时商城隐藏全部规格且不可购买。
	Hidden          int                    `json:"hidden"`
	RoleIDs         []int64                `json:"roleIds"`
	RoleNames       []string               `json:"roleNames"`
	RoleRestricted  int                    `json:"roleRestricted"`
	RoleVisibleOnly int                    `json:"roleVisibleOnly"`
	SpecCount       int                    `json:"specCount"`
	Specs           []InterfacePackageSpec `json:"specs"`
	Status          int                    `json:"status"`
	UpdateTime      string                 `json:"updateTime"`
}

type InterfacePackageSpec struct {
	CreateTime string `json:"createTime"`
	DailyLimit int64  `json:"dailyLimit"`
	// TotalLimit 为规格生命周期总调用量上限，0 = 不限；CountMode 同全站套餐。
	TotalLimit   int64  `json:"totalLimit"`
	CountMode    string `json:"countMode"`
	ID           string `json:"id,omitempty"`
	PackageID    string `json:"packageId,omitempty"`
	PerUserLimit int    `json:"perUserLimit"`
	Price        string `json:"price"`
	QPSLimit     int    `json:"qpsLimit"`
	Remark       string `json:"remark,omitempty"`
	SaleEndAt    string `json:"saleEndAt,omitempty"`
	SaleStartAt  string `json:"saleStartAt,omitempty"`
	SortNo       int    `json:"sortNo"`
	SpecName     string `json:"specName"`
	Status       int    `json:"status"`
	StockLimit   int    `json:"stockLimit"`
	StockUsed    int    `json:"stockUsed"`
	UpdateTime   string `json:"updateTime"`
	ValidDays    int    `json:"validDays"`
}

type SaveInterfacePackageRequest struct {
	InterfaceID     int64                             `json:"interfaceId"`
	Name            string                            `json:"name"`
	Remark          string                            `json:"remark"`
	RoleIDs         []int64                           `json:"roleIds"`
	RoleRestricted  int                               `json:"roleRestricted"`
	RoleVisibleOnly int                               `json:"roleVisibleOnly"`
	Hidden          *int                              `json:"hidden"`
	Specs           []SaveInterfacePackageSpecRequest `json:"specs"`
	Status          *int                              `json:"status"`
}

type SaveInterfacePackageSpecRequest struct {
	DailyLimit   int64  `json:"dailyLimit"`
	TotalLimit   int64  `json:"totalLimit"`
	CountMode    string `json:"countMode"`
	ID           string `json:"id,omitempty"`
	Name         string `json:"name,omitempty"`
	PerUserLimit int    `json:"perUserLimit"`
	Price        any    `json:"price"`
	QPSLimit     int    `json:"qpsLimit"`
	Remark       string `json:"remark"`
	SaleEndAt    string `json:"saleEndAt"`
	SaleStartAt  string `json:"saleStartAt"`
	SortNo       int    `json:"sortNo"`
	SpecName     string `json:"specName"`
	Status       *int   `json:"status"`
	StockLimit   int    `json:"stockLimit"`
	ValidDays    int    `json:"validDays"`
}

type PointPackage struct {
	CanPurchase     bool     `json:"canPurchase"`
	CreateTime      string   `json:"createTime"`
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	PerUserLimit    int      `json:"perUserLimit"`
	PointAmount     int64    `json:"pointAmount"`
	Points          int64    `json:"points"`
	Price           string   `json:"price"`
	Remark          string   `json:"remark,omitempty"`
	RoleIDs         []int64  `json:"roleIds"`
	RoleNames       []string `json:"roleNames"`
	RoleRestricted  int      `json:"roleRestricted"`
	RoleVisibleOnly int      `json:"roleVisibleOnly"`
	// Hidden = 1 时商城列表过滤且不可购买，仅管理端可见、可下发。
	Hidden      int    `json:"hidden"`
	SaleEndAt   string `json:"saleEndAt,omitempty"`
	SaleStartAt string `json:"saleStartAt,omitempty"`
	Status      int    `json:"status"`
	StockLimit  int    `json:"stockLimit"`
	StockUsed   int    `json:"stockUsed"`
	UpdateTime  string `json:"updateTime"`
}

type SavePointPackageRequest struct {
	Name            string  `json:"name"`
	PerUserLimit    int     `json:"perUserLimit"`
	PointAmount     any     `json:"pointAmount"`
	Points          any     `json:"points"`
	Price           any     `json:"price"`
	Remark          string  `json:"remark"`
	RoleIDs         []int64 `json:"roleIds"`
	RoleRestricted  int     `json:"roleRestricted"`
	RoleVisibleOnly int     `json:"roleVisibleOnly"`
	Hidden          *int    `json:"hidden"`
	SaleEndAt       string  `json:"saleEndAt"`
	SaleStartAt     string  `json:"saleStartAt"`
	Status          *int    `json:"status"`
	StockLimit      int     `json:"stockLimit"`
}

type UserPackage struct {
	DailyLimit int64 `json:"dailyLimit"`
	// TotalLimit 为总调用量快照，0 = 不限；TotalUsed 为累计消耗。
	TotalLimit int64  `json:"totalLimit"`
	TotalUsed  int64  `json:"totalUsed"`
	CountMode  string `json:"countMode"`
	EndAt      string `json:"endAt"`
	ID         string `json:"id"`
	// IsPrimary 表示该权益是用户指定的主套餐：网关优先扣减它，
	// 直到日限用完或到期，再按「快到期的、高配的」默认顺序回落。
	IsPrimary bool   `json:"isPrimary"`
	Name      string `json:"name"`
	QPSLimit  int    `json:"qpsLimit"`
	Scope     string `json:"scope"`
	UsedToday int64  `json:"usedToday"`
	// PackageID / SpecID 是购买来源，前端据此判断「用户是否已持有同款」，
	// 从而把按钮文案从「购买」切成「续费」。
	PackageID string `json:"packageId,omitempty"`
	SpecID    string `json:"specId,omitempty"`
	// RemainDays 为剩余天数（向上取整，当天算 1 天）；Renewable 表示该套餐
	// 当前允许续费顺延。两者都是展示字段，不参与网关扣减判定。
	RemainDays int  `json:"remainDays"`
	Renewable  bool `json:"renewable"`
}

// SetPrimaryPackageRequest 用于设置/取消主套餐。
// ID 传 0（或省略）表示取消该层的所有主套餐标记。
type SetPrimaryPackageRequest struct {
	Scope string `json:"scope"`
	ID    int64  `json:"id"`
}

// UserPackageDetail 是用户权益的富快照，专供开放 API /open-api/account/packages
// 使用（含规格名/接口编号/起始时间/状态等字段）。与「我的套餐」同源同表，
// 仅投影字段更全；不改动既有 UserPackages 的语义与调用方。
type UserPackageDetail struct {
	Kind        string // global | interface
	PackageName string
	SpecName    string
	InterfaceID int64
	DailyLimit  int64
	TotalLimit  int64
	TotalUsed   int64
	CountMode   string
	QPSLimit    int
	StartAt     string
	EndAt       string
	DayUsed     int64
	Status      int
}

// UserEntitlement 是管理端看到的单条用户套餐权益（含用户与接口信息）。
type UserEntitlement struct {
	APICode       string `json:"apiCode,omitempty"`
	CreateTime    string `json:"createTime"`
	DailyLimit    int64  `json:"dailyLimit"`
	TotalLimit    int64  `json:"totalLimit"`
	CountMode     string `json:"countMode"`
	DayUsed       int64  `json:"dayUsed"`
	DisplayName   string `json:"displayName,omitempty"`
	EndAt         string `json:"endAt"`
	ID            string `json:"id"`
	InterfaceID   string `json:"interfaceId,omitempty"`
	InterfaceName string `json:"interfaceName,omitempty"`
	PackageID     string `json:"packageId"`
	PackageName   string `json:"packageName"`
	QPSLimit      int    `json:"qpsLimit"`
	Scope         string `json:"scope"` // GLOBAL | INTERFACE
	SpecID        string `json:"specId,omitempty"`
	SpecName      string `json:"specName,omitempty"`
	StartAt       string `json:"startAt"`
	Status        int    `json:"status"`
	TotalUsed     int64  `json:"totalUsed"`
	UserID        string `json:"userId"`
	Username      string `json:"username"`
}

type ListUserEntitlementParams struct {
	Keyword  string
	Page     int
	PageSize int
	Scope    string // '' | GLOBAL | INTERFACE
	Status   *int
}

// UpdateUserEntitlementRequest 管理端编辑单条用户套餐权益：
// 所有字段可选，未出现的字段保持原值；至少提供一个变更字段。
type UpdateUserEntitlementRequest struct {
	Scope      string `json:"scope"`
	Status     *int   `json:"status"`
	EndAt      string `json:"endAt,omitempty"` // YYYY-MM-DD HH:mm:ss
	DailyLimit *int64 `json:"dailyLimit,omitempty"`
	TotalLimit *int64 `json:"totalLimit,omitempty"`
	CountMode  string `json:"countMode,omitempty"` // success | attempt
	QPSLimit   *int   `json:"qpsLimit,omitempty"`
}

// GrantUserEntitlementRequest 管理端向用户直接下发套餐权益（不扣余额、不走商城）。
// Scope=GLOBAL 时取 PackageID；Scope=INTERFACE 时取 SpecID；Scope=POINT 时取 PackageID。
// Users 是用户名或数字用户 ID 的混合列表，每人各发放一份；重复提交会叠加发放。
// EndAt 可选（YYYY-MM-DD HH:mm:ss），传入后覆盖套餐自带的有效天数。
type GrantUserEntitlementRequest struct {
	Scope     string   `json:"scope"`
	PackageID int64    `json:"packageId"`
	SpecID    int64    `json:"specId"`
	Users     []string `json:"users"`
	EndAt     string   `json:"endAt"`
}

// GrantUserEntitlementResult 下发结果：Granted 为实际发放份数，
// NotFound 列出既不是有效用户名也不是有效用户 ID 的条目。
type GrantUserEntitlementResult struct {
	Granted  int      `json:"granted"`
	NotFound []string `json:"notFound"`
}

type WalletTransaction struct {
	Amount       string `json:"amount"`
	BalanceAfter string `json:"balanceAfter"`
	BizID        string `json:"bizId,omitempty"`
	BizType      string `json:"bizType"`
	CreateTime   string `json:"createTime"`
	ID           string `json:"id"`
	Points       int64  `json:"points"`
	PointsAfter  int64  `json:"pointsAfter"`
	Remark       string `json:"remark,omitempty"`
}

type BillingRecord struct {
	APICode      string `json:"apiCode"`
	ChargeAmount string `json:"chargeAmount"`
	ChargeType   string `json:"chargeType"`
	CreateTime   string `json:"createTime"`
	ID           string `json:"id"`
	InterfaceID  string `json:"interfaceId"`
	PointAmount  int64  `json:"pointAmount"`
	Reason       string `json:"reason,omitempty"`
	Status       string `json:"status"`
	UserID       string `json:"userId"`
}
