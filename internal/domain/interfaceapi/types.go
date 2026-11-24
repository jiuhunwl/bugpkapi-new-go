package interfaceapi

import (
	"bugpk-api/internal/domain/keyaccess"
	"bugpk-api/internal/pkg/domainrouting"
	"bugpk-api/internal/pkg/jsoncleaning"
	"bugpk-api/internal/pkg/proxypool"
	"bugpk-api/internal/pkg/requestvalidation"
)

type Principal struct {
	ID           int64
	Username     string
	Roles        []string
	TokenVersion int
}

type ListParams struct {
	FeaturedOnly  *int
	IsFeatured    *int
	Keyword       string
	Page          int
	PageSize      int
	PriceType     string
	RequestMethod string
	Status        *int
}

type PageResult[T any] struct {
	Items []T   `json:"items"`
	Total int64 `json:"total"`
}

type InterfaceItem struct {
	BillingEnabled          int                         `json:"billingEnabled"`
	BillingLogEnabled       int                         `json:"billingLogEnabled"`
	APICode                 string                      `json:"apiCode"`
	AvatarURL               string                      `json:"avatarUrl,omitempty"`
	CallCount               int64                       `json:"callCount"`
	CreateTime              string                      `json:"createTime,omitempty"`
	CurrentNode             int                         `json:"currentNode,omitempty"`
	CurrentNodeURL          string                      `json:"currentNodeUrl,omitempty"`
	DomainRouting           DomainRoutingConfig         `json:"domainRouting,omitempty"`
	Description             string                      `json:"description,omitempty"`
	DocNotice               string                      `json:"docNotice,omitempty"`
	DocPreferredMethod      string                      `json:"docPreferredMethod,omitempty"`
	DocRequestParams        string                      `json:"docRequestParams,omitempty"`
	DocResponseExample      string                      `json:"docResponseExample,omitempty"`
	DocResponseFields       string                      `json:"docResponseFields,omitempty"`
	DocResponseFormats      string                      `json:"docResponseFormats,omitempty"`
	DocResponseType         string                      `json:"docResponseType,omitempty"`
	DocStatusCodes          string                      `json:"docStatusCodes,omitempty"`
	DocSummary              string                      `json:"docSummary,omitempty"`
	GatewayPath             string                      `json:"gatewayPath,omitempty"`
	GatewayURLTemplate      string                      `json:"gatewayUrlTemplate,omitempty"`
	ID                      string                      `json:"id"`
	IsFeatured              int                         `json:"isFeatured"`
	IsTop                   int                         `json:"isTop"`
	Name                    string                      `json:"name"`
	ParamKeys               []string                    `json:"paramKeys,omitempty"`
	PointPrice              int64                       `json:"pointPrice"`
	SharedQPSLimit          int                         `json:"sharedQpsLimit"`
	QPSLimit                int                         `json:"qpsLimit"`
	PollingCheckEnabled     int                         `json:"pollingCheckEnabled,omitempty"`
	PollingCheckExpected    string                      `json:"pollingCheckExpected,omitempty"`
	PollingCheckField       string                      `json:"pollingCheckField,omitempty"`
	PollingEnabled          int                         `json:"pollingEnabled,omitempty"`
	PollingMode             string                      `json:"pollingMode,omitempty"`
	PollingStopCode         string                      `json:"pollingStopCode,omitempty"`
	ProxyPoolConfig         proxypool.Config            `json:"proxyPoolConfig,omitempty"`
	ResponseCleaningEnabled int                         `json:"responseCleaningEnabled,omitempty"`
	ResponseFieldMappings   []jsoncleaning.FieldMapping `json:"responseFieldMappings,omitempty"`
	Price                   string                      `json:"price"`
	PriceLabel              string                      `json:"priceLabel,omitempty"`
	Remark                  string                      `json:"remark,omitempty"`
	RequestMethod           string                      `json:"requestMethod"`
	// RequestTimeoutMs is the effective upstream timeout used by the admin
	// invoke/debug endpoint. Open-gateway requests resolve the same policy in
	// the gateway repository.
	RequestTimeoutMs   int              `json:"requestTimeoutMs,omitempty"`
	RequestURL         string           `json:"requestUrl"`
	ResponseType       string           `json:"responseType,omitempty"`
	Status             int              `json:"status"`
	TemplateParameters []string         `json:"templateParameters,omitempty"`
	UpdateTime         string           `json:"updateTime,omitempty"`
	UpstreamConfigs    []UpstreamConfig `json:"upstreamConfigs,omitempty"`
	UpstreamCount      int              `json:"upstreamCount,omitempty"`
	UpstreamURLs       []string         `json:"upstreamUrls,omitempty"`
	// domainRouteID is runtime-only metadata used to isolate health and
	// round-robin state after a management test call selects a sub-pool.
	domainRouteID string
}

type SaveInterfaceRequest struct {
	APICode                 string                      `json:"apiCode"`
	AvatarURL               string                      `json:"avatarUrl"`
	DomainRouting           *DomainRoutingConfig        `json:"domainRouting"`
	IsFeatured              *int                        `json:"isFeatured"`
	IsTop                   *int                        `json:"isTop"`
	Name                    string                      `json:"name"`
	PointPrice              any                         `json:"pointPrice"`
	SharedQPSLimit          *int                        `json:"sharedQpsLimit"`
	QPSLimit                *int                        `json:"qpsLimit"`
	PollingCheckEnabled     *int                        `json:"pollingCheckEnabled"`
	PollingCheckExpected    string                      `json:"pollingCheckExpected"`
	PollingCheckField       string                      `json:"pollingCheckField"`
	PollingEnabled          *int                        `json:"pollingEnabled"`
	PollingMode             string                      `json:"pollingMode"`
	PollingStopCode         string                      `json:"pollingStopCode"`
	ProxyPoolConfig         proxypool.Config            `json:"proxyPoolConfig"`
	ResponseCleaningEnabled *int                        `json:"responseCleaningEnabled"`
	ResponseFieldMappings   []jsoncleaning.FieldMapping `json:"responseFieldMappings"`
	Price                   any                         `json:"price"`
	Remark                  string                      `json:"remark"`
	RequestMethod           string                      `json:"requestMethod"`
	RequestURL              string                      `json:"requestUrl"`
	Status                  *int                        `json:"status"`
	UpstreamConfigs         []UpstreamConfig            `json:"upstreamConfigs"`
	UpstreamURLs            []string                    `json:"upstreamUrls"`
}

type BatchInterfaceStatusRequest struct {
	IDs    []int64 `json:"ids"`
	Status int     `json:"status"`
}

// InterfaceQuotaPolicy is the admin-editable, per-interface request quota.
// Limits are 0 = unlimited. The global (all-users) caps are not role-specific;
// role overrides only adjust the per-user caps.
type InterfaceQuotaPolicy struct {
	Enabled   int    `json:"enabled"`
	CountMode string `json:"countMode"`
	// CoverPackage 为 1 时接口配额同样约束套餐用户；默认 0 表示套餐用户豁免，
	// 已由套餐额度承接的请求整层跳过本层，保留「套餐优先」。
	CoverPackage  int                  `json:"coverPackage"`
	PerUserHour   int64                `json:"perUserHour"`
	PerUserDay    int64                `json:"perUserDay"`
	PerUserWeek   int64                `json:"perUserWeek"`
	PerUserMonth  int64                `json:"perUserMonth"`
	AllUsersHour  int64                `json:"allUsersHour"`
	AllUsersDay   int64                `json:"allUsersDay"`
	AllUsersWeek  int64                `json:"allUsersWeek"`
	AllUsersMonth int64                `json:"allUsersMonth"`
	RoleOverrides []InterfaceRoleQuota `json:"roleOverrides"`
}

// InterfaceRoleQuota is one role's override of the interface's per-user caps.
//
//	inherit   -> use interface defaults
//	unlimited -> exempt this role from per-user caps (platform total still counts)
//	ignore    -> skip this layer entirely for the role: no check, no counting,
//	             including the platform-wide total
//	adjust    -> default + signed delta (negative delta floors at 1)
//	override  -> absolute per-user values (0 = unlimited)
type InterfaceRoleQuota struct {
	RoleID        int64  `json:"roleId"`
	RoleKey       string `json:"roleKey,omitempty"`
	RoleName      string `json:"roleName,omitempty"`
	Mode          string `json:"mode"`
	AdjustHour    int64  `json:"adjustHour"`
	AdjustDay     int64  `json:"adjustDay"`
	AdjustWeek    int64  `json:"adjustWeek"`
	AdjustMonth   int64  `json:"adjustMonth"`
	OverrideHour  int64  `json:"overrideHour"`
	OverrideDay   int64  `json:"overrideDay"`
	OverrideWeek  int64  `json:"overrideWeek"`
	OverrideMonth int64  `json:"overrideMonth"`
}

// InterfaceMeteringPolicy 是接口的三维计量策略。每一维的 inherit 都表示沿用既有
// 判定链（billing_enabled + 价格 + 角色配额 + 套餐优先），因此未配置策略的接口
// 行为与引入本表之前完全一致。
//
//	asset_mode   inherit -> 按 billing_enabled 与价格；free -> 永不扣余额/点数；charge -> 有价格即必扣
//	package_mode inherit -> 有可用套餐就扣；consume -> 强制计入套餐；skip -> 不占套餐次数
//	group_mode   inherit -> 按角色配额开关计数；count -> 角色关闭配额也计数；skip -> 不计入免费额度窗口
//
// 兜底闸门不受策略约束：注册后 24h 首日额度与接口全平台总量始终生效。
type InterfaceMeteringPolicy struct {
	AssetMode     string                  `json:"assetMode"`
	PackageMode   string                  `json:"packageMode"`
	GroupMode     string                  `json:"groupMode"`
	RoleOverrides []InterfaceRoleMetering `json:"roleOverrides"`
	// GuardAvailable 报告该接口是否已启用接口配额并设置了全平台总量。
	// 三维全部 skip 时这是唯一兜底，读取时一并返回，供前端提前给出提示。
	GuardEnabled     int  `json:"guardEnabled"`
	GuardAllUsersSet bool `json:"guardAllUsersSet"`
}

// InterfaceRoleMetering 是某个用户组对三维策略的覆盖；每一维 inherit 表示沿用接口级。
type InterfaceRoleMetering struct {
	RoleID      int64  `json:"roleId"`
	RoleKey     string `json:"roleKey,omitempty"`
	RoleName    string `json:"roleName,omitempty"`
	AssetMode   string `json:"assetMode"`
	PackageMode string `json:"packageMode"`
	GroupMode   string `json:"groupMode"`
}

// InterfaceDegradePolicy 是接口的按用户组次数降级配置。
//
// 源接口 A 达到某个用户组的阈值后，后续请求仍走 A 的网关路径，但上游执行
// 配置切换为默认目标接口（或该用户组单独覆盖的目标接口）。鉴权、计费、
// 配额、限流和调用日志仍按 A 执行，因此本策略只描述「何时切换上游」。
type InterfaceDegradePolicy struct {
	Enabled                    int    `json:"enabled"`
	DefaultTargetInterfaceID   int64  `json:"defaultTargetInterfaceId"`
	DefaultTargetInterfaceCode string `json:"defaultTargetInterfaceCode,omitempty"`
	DefaultTargetInterfaceName string `json:"defaultTargetInterfaceName,omitempty"`
	// DefaultTargetURL 是自定义内部地址；与 DefaultTargetInterfaceID 二选一。
	DefaultTargetURL string                 `json:"defaultTargetUrl,omitempty"`
	PolicyVersion    int64                  `json:"policyVersion,omitempty"`
	RoleOverrides    []InterfaceRoleDegrade `json:"roleOverrides"`
}

// InterfaceRoleDegrade 是单个用户组的降级阈值。
//
//	window    day/week/month/total
//	threshold 达到该次数后降级；0 表示该用户组不降级
//	target    0 表示跟随接口默认目标
type InterfaceRoleDegrade struct {
	RoleID              int64  `json:"roleId"`
	RoleKey             string `json:"roleKey,omitempty"`
	RoleName            string `json:"roleName,omitempty"`
	Window              string `json:"window"`
	Threshold           int64  `json:"threshold"`
	TargetInterfaceID   int64  `json:"targetInterfaceId,omitempty"`
	TargetInterfaceCode string `json:"targetInterfaceCode,omitempty"`
	TargetInterfaceName string `json:"targetInterfaceName,omitempty"`
	// TargetURL 是自定义内部地址；为空表示跟随接口默认目标。
	TargetURL string `json:"targetUrl,omitempty"`
}

type BatchInterfaceIDsRequest struct {
	IDs []int64 `json:"ids"`
}

type PollingConfig struct {
	CurrentNode             int                         `json:"currentNode"`
	CurrentNodeURL          string                      `json:"currentNodeUrl"`
	DomainRouting           DomainRoutingConfig         `json:"domainRouting"`
	ID                      string                      `json:"id"`
	Name                    string                      `json:"name"`
	PollingCheckEnabled     int                         `json:"pollingCheckEnabled"`
	PollingCheckExpected    string                      `json:"pollingCheckExpected"`
	PollingCheckField       string                      `json:"pollingCheckField"`
	PollingEnabled          int                         `json:"pollingEnabled"`
	PollingMode             string                      `json:"pollingMode"`
	PollingStopCode         string                      `json:"pollingStopCode"`
	ProxyPoolConfig         proxypool.Config            `json:"proxyPoolConfig"`
	ResponseCleaningEnabled int                         `json:"responseCleaningEnabled"`
	ResponseFieldMappings   []jsoncleaning.FieldMapping `json:"responseFieldMappings"`
	RequestURL              string                      `json:"requestUrl"`
	UpstreamConfigs         []UpstreamConfig            `json:"upstreamConfigs"`
	UpstreamCount           int                         `json:"upstreamCount"`
	UpstreamURLs            []string                    `json:"upstreamUrls"`
}

type UpstreamConfig struct {
	// Tracks an omitted JSON toggle for partial general-form updates; never persisted.
	pollingCheckMissing   bool
	FailureCount          int                         `json:"failureCount,omitempty"`
	HealthStatus          string                      `json:"healthStatus,omitempty"`
	LastCheckedAt         string                      `json:"lastCheckedAt,omitempty"`
	LastLatencyMs         int64                       `json:"lastLatencyMs,omitempty"`
	PollingCheckEnabled   int                         `json:"pollingCheckEnabled"`
	PollingCheckExpected  string                      `json:"pollingCheckExpected"`
	PollingCheckField     string                      `json:"pollingCheckField"`
	ResponseFieldMappings []jsoncleaning.FieldMapping `json:"responseFieldMappings,omitempty"`
	URL                   string                      `json:"url"`
	// pollingCheckAuto marks the compatibility check applied to legacy
	// polling rows that have no explicit response-check toggle. It is internal
	// runtime state and must not be serialized in API responses.
	pollingCheckAuto bool
}

type DomainRoutingConfig struct {
	Enabled        int                  `json:"enabled"`
	Fallback       string               `json:"fallback"`
	DefaultRouteID string               `json:"defaultRouteId,omitempty"`
	Routes         []DomainPollingRoute `json:"routes,omitempty"`
}

type DomainPollingRoute struct {
	ID                      string `json:"id"`
	Name                    string `json:"name"`
	Enabled                 int    `json:"enabled"`
	Priority                int    `json:"priority"`
	prioritySet             bool
	Match                   domainrouting.MatchConfig   `json:"match"`
	RequestURL              string                      `json:"requestUrl"`
	PollingEnabled          int                         `json:"pollingEnabled"`
	PollingMode             string                      `json:"pollingMode"`
	PollingCheckEnabled     int                         `json:"pollingCheckEnabled"`
	PollingCheckExpected    string                      `json:"pollingCheckExpected"`
	PollingCheckField       string                      `json:"pollingCheckField"`
	PollingStopCode         string                      `json:"pollingStopCode"`
	ResponseCleaningEnabled int                         `json:"responseCleaningEnabled"`
	ResponseFieldMappings   []jsoncleaning.FieldMapping `json:"responseFieldMappings,omitempty"`
	UpstreamConfigs         []UpstreamConfig            `json:"upstreamConfigs,omitempty"`
}

type SavePollingConfig struct {
	DomainRouting           *DomainRoutingConfig        `json:"domainRouting"`
	PollingCheckEnabled     *int                        `json:"pollingCheckEnabled"`
	PollingCheckExpected    string                      `json:"pollingCheckExpected"`
	PollingCheckField       string                      `json:"pollingCheckField"`
	PollingEnabled          *int                        `json:"pollingEnabled"`
	PollingMode             string                      `json:"pollingMode"`
	PollingStopCode         string                      `json:"pollingStopCode"`
	ProxyPoolConfig         proxypool.Config            `json:"proxyPoolConfig"`
	ResponseCleaningEnabled *int                        `json:"responseCleaningEnabled"`
	ResponseFieldMappings   []jsoncleaning.FieldMapping `json:"responseFieldMappings"`
	UpstreamConfigs         []UpstreamConfig            `json:"upstreamConfigs"`
	UpstreamURLs            []string                    `json:"upstreamUrls"`
}

type UpstreamCheckResult struct {
	RouteID      string `json:"routeId,omitempty"`
	RouteName    string `json:"routeName,omitempty"`
	URL          string `json:"url"`
	HealthStatus string `json:"healthStatus"`
	LatencyMs    int64  `json:"latencyMs,omitempty"`
	StatusCode   int    `json:"statusCode,omitempty"`
	Message      string `json:"message,omitempty"`
	CheckedAt    string `json:"checkedAt,omitempty"`
}

type ProxyPoolInspectRequest struct {
	InterfaceID     int64            `json:"interfaceId"`
	ProxyPoolConfig proxypool.Config `json:"proxyPoolConfig"`
}

type ProxyPoolInspectResult struct {
	Candidates   []proxypool.Candidate `json:"candidates"`
	DetectedPath string                `json:"detectedPath"`
	Host         string                `json:"host"`
	Port         int                   `json:"port"`
	Protocol     string                `json:"protocol"`
}

type ProxyPoolTestRequest struct {
	InterfaceID     int64            `json:"interfaceId"`
	ProxyPoolConfig proxypool.Config `json:"proxyPoolConfig"`
	// TestTarget 指定代理连通性/出口检测目标（如 bilibili/baidu/douyin/custom）。
	// 为空时使用服务端默认目标。
	TestTarget string `json:"testTarget,omitempty"`
	// CustomTestUrl 在 TestTarget=custom 时使用，必须是公网 http(s) 地址。
	CustomTestUrl string `json:"customTestUrl,omitempty"`
}

type ProxyPoolTestResult struct {
	ElapsedMs    int64  `json:"elapsedMs"`
	ExitIP       string `json:"exitIp"`
	Protocol     string `json:"protocol"`
	ProxyAddress string `json:"proxyAddress"`
	StatusCode   int    `json:"statusCode"`
	Success      bool   `json:"success"`
	// Target 回显实际使用的测试目标 URL（便于前端展示与排查）。
	Target string `json:"target,omitempty"`
}

type ProbeTarget struct {
	DomainRouting           DomainRoutingConfig
	ID                      string
	RequestURL              string
	RequestMethod           string
	PollingEnabled          int
	PollingMode             string
	PollingStopCode         string
	ProxyPoolConfig         proxypool.Config
	UpstreamConfigs         []UpstreamConfig
	PollingCheckEnabled     int
	PollingCheckField       string
	PollingCheckExpected    string
	ResponseCleaningEnabled int
	ResponseFieldMappings   []jsoncleaning.FieldMapping
}

type DocConfig struct {
	APICode            string   `json:"apiCode,omitempty"`
	DocNotice          string   `json:"docNotice,omitempty"`
	DocPreferredMethod string   `json:"docPreferredMethod,omitempty"`
	DocRequestParams   string   `json:"docRequestParams,omitempty"`
	DocResponseExample string   `json:"docResponseExample,omitempty"`
	DocResponseFields  string   `json:"docResponseFields,omitempty"`
	DocResponseType    string   `json:"docResponseType,omitempty"`
	DocStatusCodes     string   `json:"docStatusCodes,omitempty"`
	DocSummary         string   `json:"docSummary,omitempty"`
	ID                 string   `json:"id,omitempty"`
	Name               string   `json:"name,omitempty"`
	RequestMethod      string   `json:"requestMethod,omitempty"`
	RequestURL         string   `json:"requestUrl,omitempty"`
	TemplateParameters []string `json:"templateParameters,omitempty"`

	// DocResponseFormats is the multi-format docs response. A nil slice means
	// "not provided" and preserves the stored column (older clients); a
	// non-nil slice replaces it entirely.
	DocResponseFormats []DocResponseFormat `json:"docResponseFormats,omitempty"`
}

// DocResponseFormat is one declared public docs response format. Type is one
// of JSON/TEXT/XML/HTML/FILE/SSE.
type DocResponseFormat struct {
	Type    string `json:"type"`
	Example string `json:"example,omitempty"`
}

type BillingRule struct {
	ExpectedValue string `json:"expectedValue"`
	FieldName     string `json:"fieldName"`
	ID            string `json:"id,omitempty"`
	Operator      string `json:"operator"`
	SortNo        int    `json:"sortNo,omitempty"`
}

type SaveBillingRulesRequest struct {
	// Omitted switches preserve existing settings for older clients.
	BillingEnabled    *int          `json:"billingEnabled"`
	BillingLogEnabled *int          `json:"billingLogEnabled"`
	Rules             []BillingRule `json:"rules"`
}

type RequestValidationPolicy struct {
	requestvalidation.Policy
	ID   string `json:"id"`
	Name string `json:"name"`
}

type SaveRequestValidationPolicyRequest struct {
	DefaultResult requestvalidation.Result `json:"defaultResult"`
	Enabled       *int                     `json:"enabled"`
	Rules         []requestvalidation.Rule `json:"rules"`
}

type ResponseCacheRule struct {
	ExpectedValue string `json:"expectedValue"`
	FieldName     string `json:"fieldName"`
	Name          string `json:"name"`
	Operator      string `json:"operator"`
	SortNo        int    `json:"sortNo"`
	StatusCode    int    `json:"statusCode"`
	TTLSeconds    int    `json:"ttlSeconds"`
}

type DeliveryPolicy struct {
	CacheDefaultTTLSeconds    int                 `json:"cacheDefaultTtlSeconds"`
	CacheEnabled              int                 `json:"cacheEnabled"`
	CachePolicyOverride       int                 `json:"cachePolicyOverride"`
	CacheRules                []ResponseCacheRule `json:"cacheRules"`
	ID                        string              `json:"id"`
	Name                      string              `json:"name"`
	PolicyVersion             int64               `json:"policyVersion,omitempty"`
	ResponseAdEnabled         int                 `json:"responseAdEnabled"`
	ResponseAdField           string              `json:"responseAdField"`
	ResponseAdID              int64               `json:"responseAdId,string"`
	ResponseAdPolicyOverride  int                 `json:"responseAdPolicyOverride"`
	RequestTimeoutMs          int                 `json:"requestTimeoutMs"`
	RequestTimeoutOverride    int                 `json:"requestTimeoutOverride"`
	EffectiveRequestTimeoutMs int                 `json:"effectiveRequestTimeoutMs"`
	Scope                     string              `json:"scope,omitempty"`
}

type SaveDeliveryPolicyRequest struct {
	CacheDefaultTTLSeconds   int                 `json:"cacheDefaultTtlSeconds"`
	CacheEnabled             *int                `json:"cacheEnabled"`
	CachePolicyOverride      *int                `json:"cachePolicyOverride"`
	CacheRules               []ResponseCacheRule `json:"cacheRules"`
	ResponseAdEnabled        *int                `json:"responseAdEnabled"`
	ResponseAdField          string              `json:"responseAdField"`
	ResponseAdID             any                 `json:"responseAdId"`
	ResponseAdPolicyOverride *int                `json:"responseAdPolicyOverride"`
	RequestTimeoutMs         int                 `json:"requestTimeoutMs"`
	RequestTimeoutOverride   *int                `json:"requestTimeoutOverride"`
	requestTimeoutMissing    bool
}

type ResponseAdvertisementOption struct {
	EndTime   string `json:"endTime,omitempty"`
	ID        string `json:"id"`
	Name      string `json:"name"`
	StartTime string `json:"startTime,omitempty"`
	Status    int    `json:"status"`
	Title     string `json:"title"`
}

type InvokePayload struct {
	Body        any            `json:"body"`
	KeyID       int64          `json:"keyId"`
	Method      string         `json:"method"`
	QueryParams map[string]any `json:"queryParams"`
}

type InvokeResult struct {
	RequestID          string `json:"requestId"`
	APIName            string `json:"apiName"`
	Billable           bool   `json:"billable"`
	Body               string `json:"body"`
	ChargeAmount       string `json:"chargeAmount"`
	ElapsedMs          int64  `json:"elapsedMs"`
	PointPrice         int64  `json:"pointPrice"`
	PollingMode        string `json:"pollingMode,omitempty"`
	Price              string `json:"price"`
	RequestMethod      string `json:"requestMethod"`
	RequestSize        int64  `json:"requestSize"`
	ResponseSize       int64  `json:"responseSize"`
	StatusCode         int    `json:"statusCode"`
	ResponseNormalized bool   `json:"-"`
	Success            bool   `json:"success"`
	UpstreamSwitched   bool   `json:"upstreamSwitched"`
	UpstreamURL        string `json:"upstreamUrl,omitempty"`
}

type MarketDetail struct {
	InterfaceItem
	Auth            map[string]string   `json:"auth"`
	CallTrend7d     []map[string]any    `json:"callTrend7d"`
	Notice          string              `json:"notice,omitempty"`
	Parameters      []map[string]any    `json:"parameters"`
	PreferredMethod string              `json:"preferredMethod"`
	Pricing         map[string]any      `json:"pricing"`
	ResponseExample string              `json:"responseExample,omitempty"`
	ResponseFields  []map[string]any    `json:"responseFields"`
	ResponseFormats []DocResponseFormat `json:"responseFormats,omitempty"`
	StatusCodes     []map[string]any    `json:"statusCodes"`
}

type MarketTestKey struct {
	HasKey    bool                        `json:"hasKey"`
	KeyScope  string                      `json:"keyScope"`
	Keys      []keyaccess.InvokeKeyOption `json:"keys"`
	MaskedKey string                      `json:"maskedKey,omitempty"`
	Message   string                      `json:"message"`
}
