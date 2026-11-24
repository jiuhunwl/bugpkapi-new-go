package user

type Principal struct {
	ID           int64
	Username     string
	Roles        []string
	TokenVersion int
}

type UserRecord struct {
	BanPermanent              int
	BanReason                 string
	BanUntil                  string
	ID                        int64
	Username                  string
	PasswordHash              string
	PasswordSet               bool
	RealName                  string
	Avatar                    string
	Email                     string
	Mobile                    string
	HomePath                  string
	Balance                   string
	Points                    int64
	Status                    int
	TokenVersion              int
	LastLoginTime             string
	PrimaryRoleID             int64
	CreateTime                string
	RoleIDs                   []int64
	RoleNames                 []string
	Roles                     []string
	SpecifiedResponseEnabled  int
	SpecifiedResponseBillable int
	TotalRequests             int64
}

// AccountSnapshot 是开放 API「本账号信息」聚合所需的只读账户快照：
// 余额/点数/生效状态/角色 + 开放 API 两态开关。调用方（openapi 域）已
// 通过开放 Key 鉴权并由服务端强制 owner=本人，故读取不附加 RBAC 校验。
type AccountSnapshot struct {
	UserID               int64
	Username             string
	Status               int
	Balance              string
	Points               int64
	Roles                []string
	OpenAPIEnabled       bool
	OpenAPIAdminDisabled bool
}

type UserInfo struct {
	Avatar      string   `json:"avatar"`
	Desc        string   `json:"desc"`
	Email       string   `json:"email,omitempty"`
	HomePath    string   `json:"homePath"`
	Mobile      string   `json:"mobile,omitempty"`
	RealName    string   `json:"realName"`
	Roles       []string `json:"roles"`
	Token       string   `json:"token"`
	UserID      string   `json:"userId"`
	Username    string   `json:"username"`
	PasswordSet bool     `json:"passwordSet"`
}

type UpdateProfileRequest struct {
	Avatar   string `json:"avatar"`
	RealName string `json:"realName"`
}

type SendBindEmailCodeRequest struct {
	Email string `json:"email"`
}

type BindEmailRequest struct {
	Code  string `json:"code"`
	Email string `json:"email"`
}

type SendBindMobileCodeRequest struct {
	Mobile string `json:"mobile"`
}

type BindMobileRequest struct {
	Code   string `json:"code"`
	Mobile string `json:"mobile"`
}

type ChangePasswordRequest struct {
	CaptchaID       string `json:"captchaId"`
	ConfirmPassword string `json:"confirmPassword"`
	NewPassword     string `json:"newPassword"`
	OldPassword     string `json:"oldPassword"`
}

type SetPasswordRequest struct {
	CaptchaID       string `json:"captchaId"`
	ConfirmPassword string `json:"confirmPassword"`
	Password        string `json:"password"`
}

type QQBindingStatus struct {
	Avatar        string `json:"avatar,omitempty"`
	BindTime      string `json:"bindTime,omitempty"`
	Bound         bool   `json:"bound"`
	Enabled       bool   `json:"enabled"`
	LastLoginTime string `json:"lastLoginTime,omitempty"`
	Nickname      string `json:"nickname,omitempty"`
}

type QQBindingAuthorizeResult struct {
	URL string `json:"url"`
}

type SystemUser struct {
	Balance                   string   `json:"balance"`
	BanPermanent              int      `json:"banPermanent"`
	BanReason                 string   `json:"banReason,omitempty"`
	BanUntil                  string   `json:"banUntil,omitempty"`
	CreateTime                string   `json:"createTime,omitempty"`
	Email                     string   `json:"email,omitempty"`
	HomePath                  string   `json:"homePath"`
	ID                        string   `json:"id"`
	LastLoginTime             string   `json:"lastLoginTime,omitempty"`
	Mobile                    string   `json:"mobile,omitempty"`
	PrimaryRoleID             int64    `json:"primaryRoleId"`
	RealName                  string   `json:"realName"`
	RoleIDs                   []int64  `json:"roleIds"`
	RoleNames                 []string `json:"roleNames"`
	Roles                     []string `json:"roles"`
	Points                    int64    `json:"points"`
	SpecifiedResponseBillable int      `json:"specifiedResponseBillable"`
	SpecifiedResponseEnabled  int      `json:"specifiedResponseEnabled"`
	Status                    int      `json:"status"`
	TotalRequests             int64    `json:"totalRequests"`
	Username                  string   `json:"username"`
}

// For updates, Balance and Points must be omitted: assets are only mutated by
// the incremental adjustment endpoint so a concurrent recharge can never be
// overwritten. Creation still accepts an initial balance and defaults missing
// assets to zero.
type SaveUserRequest struct {
	Balance        any     `json:"balance"`
	BanPermanent   *int    `json:"banPermanent"`
	BanReason      string  `json:"banReason"`
	BanUntil       string  `json:"banUntil"`
	ChangePassword bool    `json:"changePassword"`
	Email          string  `json:"email"`
	HomePath       string  `json:"homePath"`
	Mobile         string  `json:"mobile"`
	Password       string  `json:"password"`
	Points         *int64  `json:"points"`
	PrimaryRoleID  int64   `json:"primaryRoleId"`
	RealName       string  `json:"realName"`
	RoleIDs        []int64 `json:"roleIds"`
	Status         *int    `json:"status"`
	Username       string  `json:"username"`
}

type UserListParams struct {
	IncludeSensitive bool
	Keyword          string
	Page             int
	PageSize         int
	RoleID           int64
	Status           *int
}

// AdjustUserAssetRequest carries signed deltas rather than absolute values.
// Positive increases the asset, negative deducts it; zero means "no change".
type AdjustUserAssetRequest struct {
	BalanceDelta string `json:"balanceDelta"`
	PointsDelta  int64  `json:"pointsDelta"`
	Reason       string `json:"reason"`
}

type UserAssetAdjustment struct {
	Balance      string `json:"balance"`
	Points       int64  `json:"points"`
	BalanceDelta string `json:"balanceDelta"`
	PointsDelta  int64  `json:"pointsDelta"`
}

type UserListResult struct {
	Items []SystemUser `json:"items"`
	Total int64        `json:"total"`
}

type SpecifiedResponseConfig struct {
	SpecifiedResponseBillable int    `json:"specifiedResponseBillable"`
	SpecifiedResponseBody     string `json:"specifiedResponseBody"`
	SpecifiedResponseEnabled  int    `json:"specifiedResponseEnabled"`
}

type RoleOption struct {
	ID                  string   `json:"id"`
	Name                string   `json:"name"`
	Permissions         []string `json:"permissions"`
	QPSLimit            int      `json:"qpsLimit"`
	Remark              string   `json:"remark,omitempty"`
	RoleKey             string   `json:"roleKey"`
	SelfRegisterEnabled int      `json:"selfRegisterEnabled"`
	Status              int      `json:"status"`
}

// CallOutcomeStats is today's call composition. Rates deliberately use
// different denominators:
//   - success/clientError/throttle share the valid-call denominator
//     (success + client + throttle + upstream);
//   - serviceFailureRate is upstream failures over upstream-attributable calls
//     (success + upstream), so throttling or caller mistakes cannot look like
//     an upstream outage;
//   - expectedEmptyRate is the share of all calls, because a 404 is a traffic
//     outcome rather than a failed attempt.
type CallOutcomeStats struct {
	Available            bool   `json:"available"`
	Calls                int64  `json:"calls"`
	SuccessCalls         int64  `json:"successCalls"`
	ExpectedEmptyCalls   int64  `json:"expectedEmptyCalls"`
	ClientErrorCalls     int64  `json:"clientErrorCalls"`
	ThrottledCalls       int64  `json:"throttledCalls"`
	UpstreamFailureCalls int64  `json:"upstreamFailureCalls"`
	SampleCalls          int64  `json:"sampleCalls"`
	SuccessRate          string `json:"successRate"`
	ServiceFailureRate   string `json:"serviceFailureRate"`
	ClientErrorRate      string `json:"clientErrorRate"`
	ThrottleRate         string `json:"throttleRate"`
	ExpectedEmptyRate    string `json:"expectedEmptyRate"`
}

type UserWorkbenchStats struct {
	Balance               string                          `json:"balance"`
	CallTrends            map[string]UserCallTrendStats   `json:"callTrends"`
	CurrentPackageName    string                          `json:"currentPackageName"`
	CurrentPackageType    string                          `json:"currentPackageType"`
	DailyLimit            int64                           `json:"dailyLimit"`
	DailyLimitUnlimited   bool                            `json:"dailyLimitUnlimited"`
	FailedCalls           int64                           `json:"failedCalls"`
	FailedRate            string                          `json:"failedRate"`
	HasAnyPackage         bool                            `json:"hasAnyPackage"`
	HasMemberPackage      bool                            `json:"hasMemberPackage"`
	InterfacePackageCount int                             `json:"interfacePackageCount"`
	InterfacePackages     []UserWorkbenchInterfacePackage `json:"interfacePackages"`
	MemberPackage         *UserWorkbenchMemberPackage     `json:"memberPackage"`
	Outcomes              CallOutcomeStats                `json:"outcomes"`
	Points                int64                           `json:"points"`
	// PackageQuotaPreferred 为 true 表示有效配额来自套餐（主套餐优先，其次最早到期），
	// 用户组的分/时/月窗口不参与当前请求，控制台应展示「不限制」而不是角色配额。
	PackageQuotaPreferred bool   `json:"packageQuotaPreferred"`
	QPSLimit              int    `json:"qpsLimit"`
	QPSLimitUnlimited     bool   `json:"qpsLimitUnlimited"`
	RemainingCalls        int64  `json:"remainingCalls"`
	RemainingUnlimited    bool   `json:"remainingUnlimited"`
	RoleQuotaEnabled      bool   `json:"roleQuotaEnabled"`
	RoleQuotaMinuteLimit  int64  `json:"roleQuotaMinuteLimit"`
	RoleQuotaHourLimit    int64  `json:"roleQuotaHourLimit"`
	RoleQuotaDayLimit     int64  `json:"roleQuotaDayLimit"`
	RoleQuotaMonthLimit   int64  `json:"roleQuotaMonthLimit"`
	RoleQuotaCountMode    string `json:"roleQuotaCountMode"`
	RoleQuotaDayUsed      int64  `json:"roleQuotaDayUsed"`
	RoleQuotaDayRemaining int64  `json:"roleQuotaDayRemaining"`
	RoleQuotaDayUnlimited bool   `json:"roleQuotaDayUnlimited"`
	RoleKey               string `json:"roleKey"`
	RoleName              string `json:"roleName"`
	SuccessCalls          int64  `json:"successCalls"`
	SuccessRate           string `json:"successRate"`
	TodayCalls            int64  `json:"todayCalls"`
}

type UserWorkbenchMemberPackage struct {
	DailyLimit int64 `json:"dailyLimit"`
	// TotalLimit 为套餐总调用量快照，0 = 不限；TotalUsed 为累计消耗。
	TotalLimit         int64  `json:"totalLimit"`
	TotalUsed          int64  `json:"totalUsed"`
	CountMode          string `json:"countMode"`
	ExpireTime         string `json:"expireTime"`
	PackageID          string `json:"packageId"`
	PackageName        string `json:"packageName"`
	QPSLimit           int    `json:"qpsLimit"`
	RemainingCalls     int64  `json:"remainingCalls"`
	RemainingUnlimited bool   `json:"remainingUnlimited"`
	StartTime          string `json:"startTime"`
	TodayCalls         int64  `json:"todayCalls"`
	UserPackageID      string `json:"userPackageId"`
}

type UserWorkbenchInterfacePackage struct {
	APICode            string `json:"apiCode"`
	DailyLimit         int64  `json:"dailyLimit"`
	TotalLimit         int64  `json:"totalLimit"`
	TotalUsed          int64  `json:"totalUsed"`
	CountMode          string `json:"countMode"`
	ExpireTime         string `json:"expireTime"`
	InterfaceID        string `json:"interfaceId"`
	InterfaceName      string `json:"interfaceName"`
	PackageID          string `json:"packageId"`
	PackageName        string `json:"packageName"`
	QPSLimit           int    `json:"qpsLimit"`
	RemainingCalls     int64  `json:"remainingCalls"`
	RemainingUnlimited bool   `json:"remainingUnlimited"`
	SpecID             string `json:"specId"`
	SpecName           string `json:"specName"`
	StartTime          string `json:"startTime"`
	TodayCalls         int64  `json:"todayCalls"`
	UserPackageID      string `json:"userPackageId"`
}

type UserCallTrendStats struct {
	Labels []string `json:"labels"`
	Total  int64    `json:"total"`
	Values []int64  `json:"values"`
}

type AdminWorkbenchStats struct {
	AnalyticsReady   bool                    `json:"analyticsReady"`
	AccessLocations  []AdminAccessLocation   `json:"accessLocations"`
	AccessSummary    AdminAccessSummary      `json:"accessSummary"`
	ActiveUsers      []AdminActiveUserStats  `json:"activeUsers"`
	Alerts           []AdminWorkbenchAlert   `json:"alerts"`
	CallTrend7d      AdminCallTrend7d        `json:"callTrend7d"`
	DegradedSections []AdminWorkbenchSection `json:"degradedSections"`
	HotApis          []AdminHotAPIStats      `json:"hotApis"`
	Outcomes         CallOutcomeStats        `json:"outcomes"`
	Overview         AdminWorkbenchOverview  `json:"overview"`
	RevenueTrend7d   AdminRevenueTrend7d     `json:"revenueTrend7d"`
}

type AdminWorkbenchSection string

const (
	AdminWorkbenchSectionAccess       AdminWorkbenchSection = "access"
	AdminWorkbenchSectionActiveUsers  AdminWorkbenchSection = "activeUsers"
	AdminWorkbenchSectionAlerts       AdminWorkbenchSection = "alerts"
	AdminWorkbenchSectionCallTrend    AdminWorkbenchSection = "callTrend7d"
	AdminWorkbenchSectionHotAPIs      AdminWorkbenchSection = "hotApis"
	AdminWorkbenchSectionOverview     AdminWorkbenchSection = "overview"
	AdminWorkbenchSectionRevenueTrend AdminWorkbenchSection = "revenueTrend7d"
)

type AdminHotAPIStats struct {
	APIStatus     int    `json:"apiStatus"`
	APICode       string `json:"apiCode"`
	AvgElapsedMS  int64  `json:"avgElapsedMs"`
	BillableCalls int64  `json:"billableCalls"`
	ChargeAmount  string `json:"chargeAmount"`
	FailedCalls   int64  `json:"failedCalls"`
	ID            string `json:"id"`
	Name          string `json:"name"`
	SuccessCalls  int64  `json:"successCalls"`
	SuccessRate   string `json:"successRate"`
	TodayCalls    int64  `json:"todayCalls"`
}

type AdminActiveUserStats struct {
	ChargeAmount string `json:"chargeAmount"`
	FailedCalls  int64  `json:"failedCalls"`
	ID           string `json:"id"`
	RealName     string `json:"realName"`
	SuccessCalls int64  `json:"successCalls"`
	SuccessRate  string `json:"successRate"`
	TodayCalls   int64  `json:"todayCalls"`
	Username     string `json:"username"`
}

// AdminAccessSummary is the aggregate scope of the access atlas. It deliberately
// contains no client IPs or user identities; locations are grouped by region and
// coordinates before they leave the API.
type AdminAccessSummary struct {
	ActiveCountries int64 `json:"activeCountries"`
	ActiveLocations int64 `json:"activeLocations"`
	FailedCalls     int64 `json:"failedCalls"`
	SuccessCalls    int64 `json:"successCalls"`
	TotalCalls      int64 `json:"totalCalls"`
	UniqueIPs       int64 `json:"uniqueIps"`
	WindowDays      int   `json:"windowDays"`
}

// AdminAccessLocation is one aggregated point in the administrator access map.
// A point can be country/region-only when the provider did not return
// coordinates; the UI keeps it in the ranking while omitting it from the map.
type AdminAccessLocation struct {
	CallCount        int64  `json:"callCount"`
	City             string `json:"city,omitempty"`
	Country          string `json:"country,omitempty"`
	FailedCalls      int64  `json:"failedCalls"`
	FirstSeen        string `json:"firstSeen,omitempty"`
	ISP              string `json:"isp,omitempty"`
	LastSeen         string `json:"lastSeen,omitempty"`
	Latitude         string `json:"latitude,omitempty"`
	Longitude        string `json:"longitude,omitempty"`
	Province         string `json:"province,omitempty"`
	Region           string `json:"region"`
	RiskHits         int64  `json:"riskHits"`
	SuccessCalls     int64  `json:"successCalls"`
	UniqueIPs        int64  `json:"uniqueIps"`
	AverageElapsedMs int64  `json:"averageElapsedMs"`
}

type AdminWorkbenchOverview struct {
	// Explicit availability lets clients retain stale values without inventing zeros.
	UnavailableFields  []string `json:"unavailableFields"`
	APIEnabled         int64    `json:"apiEnabled"`
	APITotal           int64    `json:"apiTotal"`
	KeyEnabled         int64    `json:"keyEnabled"`
	KeyTotal           int64    `json:"keyTotal"`
	PendingFriendLinks int64    `json:"pendingFriendLinks"`
	PendingOrders      int64    `json:"pendingOrders"`
	TodayBillableCalls int64    `json:"todayBillableCalls"`
	TodayCalls         int64    `json:"todayCalls"`
	TodayChargeAmount  string   `json:"todayChargeAmount"`
	TodayFailedCalls   int64    `json:"todayFailedCalls"`
	TodayNewUsers      int64    `json:"todayNewUsers"`
	TodayRevenue       string   `json:"todayRevenue"`
	TodaySuccessCalls  int64    `json:"todaySuccessCalls"`
	TodaySuccessRate   string   `json:"todaySuccessRate"`
	TotalRevenue       string   `json:"totalRevenue"`
	TotalUsers         int64    `json:"totalUsers"`
}

type AdminCallTrend7d struct {
	BillableCalls []int64  `json:"billableCalls"`
	ChargeAmounts []string `json:"chargeAmounts"`
	ChargeTotal   string   `json:"chargeTotal"`
	FailedCalls   []int64  `json:"failedCalls"`
	Labels        []string `json:"labels"`
	SuccessCalls  []int64  `json:"successCalls"`
	Total         int64    `json:"total"`
	TotalCalls    []int64  `json:"totalCalls"`
}

type AdminRevenueTrend7d struct {
	Labels      []string `json:"labels"`
	OrderCounts []int64  `json:"orderCounts"`
	PaidAmounts []string `json:"paidAmounts"`
	TotalAmount string   `json:"totalAmount"`
	TotalOrders int64    `json:"totalOrders"`
}

type AdminWorkbenchAlert struct {
	Content string `json:"content"`
	Level   string `json:"level"`
	Title   string `json:"title"`
	URL     string `json:"url,omitempty"`
}

type UserCallSummary struct {
	FailedCalls       int64
	SuccessCalls      int64
	TodayCalls        int64
	TodaySuccessCalls int64
	TotalCalls        int64
}

// UserRoleQuota is the primary/default role's request quota snapshot used by
// the user console. It mirrors the gateway quota columns without importing the
// rbac package into the user domain.
type UserRoleQuota struct {
	Enabled   int
	Minute    int64
	Hour      int64
	Day       int64
	Month     int64
	CountMode string
	RoleKey   string
	RoleName  string
	// RoleID 用于按请求语言解析用户组名译文；0 表示未知（回退原文）。
	RoleID int64
}
