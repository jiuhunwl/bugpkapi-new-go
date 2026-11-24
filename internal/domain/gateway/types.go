package gateway

import (
	"time"

	"bugpk-api/internal/domain/ipregion"
	"bugpk-api/internal/pkg/calloutcome"
	"bugpk-api/internal/pkg/domainrouting"
	"bugpk-api/internal/pkg/jsoncleaning"
	"bugpk-api/internal/pkg/proxypool"
	"bugpk-api/internal/pkg/requestvalidation"
)

type PageResult[T any] struct {
	Items []T   `json:"items"`
	Total int64 `json:"total"`
}

type KeyListParams struct {
	Keyword string
	KeyType string
	// OwnerScope controls which account's keys are returned by the management
	// list. An empty value keeps the historical default (all keys for admins,
	// the current user's keys for everyone else).
	OwnerScope string
	Page       int
	PageSize   int
	Status     *int
}

const (
	keyOwnerScopeAll  = "ALL"
	keyOwnerScopeMine = "MINE"
)

type APIKeyItem struct {
	CreateTime      string   `json:"createTime,omitempty"`
	ExpiresAt       string   `json:"expiresAt,omitempty"`
	ID              string   `json:"id"`
	InterfaceCount  int64    `json:"interfaceCount"`
	InterfaceIDs    []int64  `json:"interfaceIds,omitempty"`
	IPWhitelist     []string `json:"ipWhitelist,omitempty"`
	KeyPrefix       string   `json:"keyPrefix"`
	KeySuffix       string   `json:"keySuffix"`
	KeyType         string   `json:"keyType"`
	LastUsedAt      string   `json:"lastUsedAt,omitempty"`
	MaskedKey       string   `json:"maskedKey"`
	Name            string   `json:"name"`
	SecretAvailable bool     `json:"secretAvailable"`
	Status          int      `json:"status"`
	// SuspendedByBan 说明该 Key 的 status=0 是「用户主动禁用」还是「账号封禁联动」：
	// 0=未因封禁暂停 1=临时封禁暂停（解封自动恢复） 2=永久封禁作废（需手动启用）。
	// 前端据此显示不同的禁用原因，避免运营把封禁联动当成用户自己关的。
	SuspendedByBan   int    `json:"suspendedByBan"`
	UpdateTime       string `json:"updateTime,omitempty"`
	UserID           string `json:"userId"`
	Username         string `json:"username,omitempty"`
	WhitelistCount   int64  `json:"whitelistCount"`
	secretCiphertext string
}

type SaveKeyRequest struct {
	ExpiresAt    *string  `json:"expiresAt"`
	InterfaceIDs []int64  `json:"interfaceIds"`
	IPWhitelist  []string `json:"ipWhitelist"`
	KeyType      string   `json:"keyType"`
	Name         string   `json:"name"`
	Status       *int     `json:"status"`
}

type CreateKeyResult struct {
	APIKeyItem
	SecretKey string `json:"secretKey"`
}

type KeySecretResult struct {
	SecretKey string `json:"secretKey"`
}

type StatusRequest struct {
	Status int `json:"status"`
}

type InterfaceIDsRequest struct {
	InterfaceIDs []int64 `json:"interfaceIds"`
}

type IPWhitelistRequest struct {
	IPWhitelist []string `json:"ipWhitelist"`
}

type KeyAuth struct {
	BanPermanent int
	BanReason    string
	BanUntil     string
	ExpiresAt    *time.Time
	ID           int64
	KeyPrefix    string
	KeyType      string
	Status       int
	UserID       int64
	UserStatus   int
	Username     string
	TokenVersion int

	// Role quota snapshot. role_config is resolved from the user's primary role
	// (falling back to the default registration role) and cached with the key
	// for the same short TTL, so an admin change takes effect within seconds.
	RoleID             int64
	RoleKey            string
	UserCreatedAt      time.Time
	QuotaEnabled       int
	QuotaMinuteLimit   int64
	QuotaHourLimit     int64
	QuotaDayLimit      int64
	QuotaMonthLimit    int64
	QuotaCountMode     string
	NewAccountDayLimit int64
	// HasUsable*Package 表示用户名下存在仍可扣减的套餐。套餐优先于用户组
	// 免费额度：默认按到期最早扣减，显式指定的主套餐（is_primary=1）排最前。
	HasUsableGlobalPackage    bool
	HasUsableInterfacePackage bool
	// Quarantined is true while bp_user_risk_state.quarantine_until is in the
	// future. A quarantined account can sign in but cannot call the gateway.
	Quarantined bool

	// HasIPWhitelist reports whether the key owns any IP whitelist rules, so
	// the hot path can skip the whitelist query when none exist.
	HasIPWhitelist bool
	// SpecifiedResponseEnabled reports whether the user configured a
	// specified response, so the hot path can skip fetching its body unless
	// one is actually configured.
	SpecifiedResponseEnabled int
}

type InterfaceConfig struct {
	// Zero values retain legacy behavior; SQL maps explicit per-interface switches.
	BillingDisabled        bool
	BillingLogDisabled     bool
	APICode                string
	BillingRules           []BillingRule
	CacheDefaultTTLSeconds int
	CacheEnabled           int
	CachePolicyOverride    int
	CachePolicyVersion     int64
	CacheRules             []ResponseCacheRule
	DomainRouting          DomainRoutingConfig
	ID                     int64
	Name                   string
	PointPrice             int64
	SharedQPSLimit         int
	UserQPSLimit           int
	PollingCheckEnabled    int
	PollingCheckExpected   string
	PollingCheckField      string
	PollingEnabled         int
	PollingMode            string
	// PollingStopCode 配置「终止轮询」的响应码匹配规则，与 PollingCheckField 配套使用。
	// 为空表示未配置：网关保留默认行为——上游返回 HTTP 404，或轮询响应体
	// 的默认 code 字段为 404 时立即终止轮询并透传该响应（业务上资源不存在）。
	// 配置后：节点响应体 pollingCheckField 字段（默认 code）的取值等于该值时，
	// 网关同样立即终止轮询并透传当前节点响应，且不计入健康失败。
	PollingStopCode         string
	ProxyPoolConfig         proxypool.Config
	ResponseCleaningEnabled int
	Price                   string
	RequestMethod           string
	// Interface-level request quota. RoleQuotas holds per-role overrides for
	// the per-user caps; all-user caps are always interface-wide.
	QuotaEnabled int
	// QuotaCoverPackage 为 1 时接口配额同样约束套餐用户；默认 0 表示套餐用户
	// 豁免——已由套餐额度承接的请求整层跳过接口配额，保留「套餐优先」。
	QuotaCoverPackage int
	QuotaCountMode    string
	QuotaPerUser      InterfaceWindowLimits
	QuotaAllUsers     InterfaceWindowLimits
	RoleQuotas        map[int64]InterfaceRoleQuota
	// 计量策略三维。加载后是接口级取值；网关在鉴权拿到角色时先用
	// withMeteringFor 把角色覆盖合并进来，再交给下游判定。
	AssetMode                string
	PackageMode              string
	GroupMode                string
	RoleMetering             map[int64]InterfaceRoleMetering
	RequestTimeoutMs         int
	RequestURL               string
	ResponseFieldMappings    []jsoncleaning.FieldMapping
	RequestValidation        requestvalidation.Policy
	ResponseAdEnabled        int
	ResponseAdField          string
	ResponseAdID             int64
	ResponseAdPolicyOverride int
	Status                   int
	UpstreamConfigs          []UpstreamConfig
	// Degrade 是按用户组次数自动降级的运行时快照。零值表示未配置降级。
	Degrade InterfaceDegradePolicy
	// domainRouteID is runtime-only metadata identifying the selected
	// domain-routed pool for health and round-robin state isolation.
	domainRouteID string
	// degradeTargetKind is runtime-only metadata: "interface" or "custom".
	degradeTargetKind string
}

// InterfaceDegradePolicy 是源接口的降级快照。窗口与阈值按用户组保存；
// 计数口径固定为「已放行请求，成功/失败/缓存命中都计」。
type InterfaceDegradePolicy struct {
	Enabled                  int
	DefaultTargetInterfaceID int64
	DefaultTargetAPICode     string
	DefaultTargetURL         string
	PolicyVersion            int64
	RoleRules                map[int64]InterfaceDegradeRule
}

// InterfaceDegradeRule 是单个用户组的降级规则。TargetAPICode 为空时回落到
// 接口级默认目标；TargetURL 非空时直接使用自定义内部地址；Threshold <= 0
// 表示该用户组不降级。
type InterfaceDegradeRule struct {
	Window            string
	Threshold         int64
	TargetInterfaceID int64
	TargetAPICode     string
	TargetURL         string
}

type InterfaceWindowLimits struct {
	Hour  int64
	Day   int64
	Week  int64
	Month int64
}

type InterfaceRoleQuota struct {
	Mode     string
	Adjust   InterfaceWindowLimits
	Override InterfaceWindowLimits
}

type InterfaceQuotaSnapshot struct {
	Enabled      int
	CountMode    string
	CoverPackage int
	PerUser      InterfaceWindowLimits
	AllUsers     InterfaceWindowLimits
	RoleQuotas   map[int64]InterfaceRoleQuota
}

// InterfaceRoleMetering 是某个用户组对接口三维计量策略的覆盖值；空串或 inherit
// 表示沿用接口级策略。
type InterfaceRoleMetering struct {
	AssetMode   string
	PackageMode string
	GroupMode   string
}

type InterfaceMeteringSnapshot struct {
	AssetMode    string
	PackageMode  string
	GroupMode    string
	RoleMetering map[int64]InterfaceRoleMetering
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

type UpstreamConfig struct {
	FailureCount          int                         `json:"failureCount,omitempty"`
	HealthStatus          string                      `json:"healthStatus,omitempty"`
	LastCheckedAt         string                      `json:"lastCheckedAt,omitempty"`
	LastLatencyMs         int64                       `json:"lastLatencyMs,omitempty"`
	PollingCheckEnabled   int                         `json:"pollingCheckEnabled"`
	PollingCheckExpected  string                      `json:"pollingCheckExpected"`
	PollingCheckField     string                      `json:"pollingCheckField"`
	ResponseFieldMappings []jsoncleaning.FieldMapping `json:"responseFieldMappings,omitempty"`
	URL                   string                      `json:"url"`
	// pollingCheckAuto is an internal marker used for legacy polling rows that
	// did not enable the explicit response-check toggle. It is intentionally
	// unexported so the administrative API never exposes implementation state.
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

type BillingRule struct {
	ExpectedValue string
	FieldName     string
	Operator      string
	SortNo        int
}

type SpecifiedResponse struct {
	Billable int
	Body     string
	Enabled  int
}

type OpenRequest struct {
	Body      []byte
	ClientIP  string
	Headers   map[string]string
	Method    string
	Query     map[string][]string
	RawQuery  string
	UserAgent string
}

type OpenRejectionKind string

const (
	OpenRejectionNone                OpenRejectionKind = ""
	OpenRejectionInvalidParams       OpenRejectionKind = "INVALID_PARAMS"
	OpenRejectionPlatformMismatch    OpenRejectionKind = "PLATFORM_MISMATCH"
	OpenRejectionDomainRouteMismatch OpenRejectionKind = "DOMAIN_ROUTE_MISMATCH"
)

type OpenResult struct {
	APIKeyID           int64
	APICode            string
	Body               string
	Billable           bool
	BillingRecordID    int64
	BillingSnapshot    string
	BillingStatus      string
	ChargeAmount       string
	ChargeType         string
	ClientIP           string
	ContentType        string
	CacheHit           bool
	ElapsedMs          int64
	ErrorMessage       string
	InterfaceID        int64
	APIKeyPrefix       string
	PointPrice         int64
	Region             ipregion.Region
	PollingMode        string
	RequestFingerprint string
	RequestMethod      string
	RequestParams      string
	RequestSize        int64
	RequestValue       string
	ResponseSize       int64
	ResponseStatus     int
	// CallLogEligible is true only when this result represents a real API
	// service request (or an accepted cache hit). Preflight validation,
	// permission, local request construction and client-cancellation failures
	// must never enter call logs or success-rate statistics.
	CallLogEligible bool `json:"-"`
	// RejectionKind makes preflight validation policy explicit. These
	// rejections are deliberately excluded from call logs, billing, and
	// aggregate success-rate statistics.
	RejectionKind OpenRejectionKind `json:"-"`
	// ResponseNormalized indicates that the body was intentionally transformed
	// by the optional upstream response cleaner. It is internal metadata and is
	// not part of the public gateway response.
	ResponseNormalized bool `json:"-"`
	// UpstreamBusinessMsg carries the upstream's own business error text
	// (msg/message/error) when a response passes HTTP but fails the polling
	// check. It lets the gateway surface the upstream's real reason instead of
	// a hard-coded validation message. It is internal metadata only.
	UpstreamBusinessMsg string `json:"-"`
	// TriedUpstreams accumulates every upstream URL attempted during a single
	// polling request. It is used to merge multi-node failure detail into one
	// call-log row (comma-separated) instead of writing one failure per node.
	// It is internal metadata only and never part of the wire response.
	TriedUpstreams []string `json:"-"`
	// OutcomeClass overrides the inferred outcome for responses whose business
	// meaning is known before persistence. The polling stop path sets
	// ExpectedEmpty so a configured business "not found" code (HTTP 200) does
	// not count as an upstream failure. Zero lets the repository classify from
	// Success and ResponseStatus.
	OutcomeClass calloutcome.Class `json:"-"`
	// Degraded 表示本次请求由目标接口的上游承接，但鉴权、计费、配额和日志
	// 仍属于原接口。内部元数据；对外只通过响应头 X-Bugpk-Degraded 暴露。
	Degraded                 bool   `json:"-"`
	DegradeTargetInterfaceID int64  `json:"-"`
	DegradeTargetAPICode     string `json:"-"`
	DegradeTargetKind        string `json:"-"`
	Success                  bool
	TraceID                  string
	UpstreamSwitched         bool
	UpstreamURL              string
	UserID                   int64
	UserVisible              bool
	cacheCachedAt            time.Time
	cacheExpiresAt           time.Time
	cacheStored              bool
	cacheTTLSeconds          int64
}

type BillingContext struct {
	APIID        int64         `json:"apiId"`
	APICode      string        `json:"apiCode"`
	BillingRules []BillingRule `json:"billingRules"`
	KeyID        int64         `json:"keyId"`
	PointPrice   int64         `json:"pointPrice"`
	Price        string        `json:"price"`
	UserID       int64         `json:"userId"`
}

type BillingReservation struct {
	BillingRecordID int64
	ChargeAmount    string
	ChargeType      string
	PackageRecordID int64
	PointAmount     int64
	QPSLimit        int
	Reserved        bool
	Snapshot        string
}
