package openapi

// 系统开放 API 域的对外类型与常量。
//
// 与数据面接口 Key（bp_api_key）硬分离：本域凭据只用于 /open-api 平面，
// 调用方身份由 X-Open-Key 解析为 OpenPrincipal（KeyID + 归属账号 + scope 清单）。

// 端点 code 目录（稳定标识，禁止在前后端手拼路径）。第一门（scope 门）
// 以本清单为准：Key 的 scope 不含该端点 code 即 403 openapi.scope_denied。
const (
	EndpointAccountRead        = "openapi:account:read"
	EndpointPackageRead        = "openapi:package:read"
	EndpointAPIKeyWrite        = "openapi:api-key:write"
	EndpointAPIKeyRead         = "openapi:api-key:read"
	EndpointOrderCreate        = "openapi:order:create"
	EndpointRedeemWrite        = "openapi:redeem:write"
	EndpointRedeemCodeGenerate = "openapi:redeem-code:generate"
)

// EndpointCodes 是端点全集（含仅管理作用域可用的生成端点）。
var EndpointCodes = []string{
	EndpointAccountRead,
	EndpointPackageRead,
	EndpointAPIKeyWrite,
	EndpointAPIKeyRead,
	EndpointOrderCreate,
	EndpointRedeemWrite,
	EndpointRedeemCodeGenerate,
}

// IsEndpointCode 判断是否为合法端点 code（签发/更新 scope 时校验）。
func IsEndpointCode(code string) bool {
	for _, item := range EndpointCodes {
		if item == code {
			return true
		}
	}
	return false
}

// 明文前缀分级：普通开放 Key = opk_，管理员作用域 = opka_。
// 前缀仅作标识（泄露样本一眼可辨危险等级），控制永远以 scope 清单 ×
// 实时 RBAC 取交为准。
const (
	KeyMarkerNormal = "opk_"
	KeyMarkerAdmin  = "opka_"
)

// Key 状态。
const (
	KeyStatusRevoked = 0
	KeyStatusActive  = 1
)

// 掩码与有效期默认值上限。
const (
	keySecretBytes       = 32 // 32 字节 = 64 hex
	maxKeyNameLength     = 64
	maxExpireInDays      = 3650
	maxIPWhitelistHard   = 100 // 兜底硬上限（配置值仅在 0..该值内生效）
	defaultKeyNamePrefix = "open-api"
)

// OpenPrincipal 是开放 API 调用方身份（鉴权中间件注入请求上下文）。
type OpenPrincipal struct {
	KeyID        int64
	KeyName      string
	UserID       int64
	IsAdminScope bool
	Scopes       map[string]struct{}
	ClientIP     string
}

// HasScope 判断 Key 是否被授予某端点（第一门）。
func (p OpenPrincipal) HasScope(code string) bool {
	if p.Scopes == nil {
		return false
	}
	_, ok := p.Scopes[code]
	return ok
}

// OpenAPIKey 是开放 Key 的管理视图（明文仅在签发/重置响应中出现一次）。
//
// Status 是数据库原始状态（1=启用 0=吊销），不随到期翻转；Expired 由到期时间
// 派生，与鉴权链的判定同源。展示层要区分「生效中 / 已过期 / 已吊销」必须两个字段
// 一起看，只看 Status 会把过期 Key 显示成可用。
type OpenAPIKey struct {
	ID           string   `json:"id"`
	UserID       string   `json:"userId"`
	Username     string   `json:"username,omitempty"`
	Name         string   `json:"name"`
	MaskedKey    string   `json:"maskedKey"`
	IsAdminScope bool     `json:"isAdminScope"`
	Status       int      `json:"status"`
	Expired      bool     `json:"expired"`
	Scopes       []string `json:"scopes"`
	IPWhitelist  []string `json:"ipWhitelist"`
	ExpiresAt    string   `json:"expiresAt"`
	LastUsedAt   string   `json:"lastUsedAt,omitempty"`
	CreateTime   string   `json:"createTime"`
	RevokedAt    string   `json:"revokedAt,omitempty"`
}

// SwitchStatus 是本账号开放 API 的三层开关读视图（控制台页面加载时取真实状态）。
type SwitchStatus struct {
	Enabled       bool `json:"enabled"`
	AdminDisabled bool `json:"adminDisabled"`
	GlobalEnabled bool `json:"globalEnabled"`
}

// IssuedKey 是签发/重置结果（明文只此一次）。
type IssuedKey struct {
	OpenAPIKey
	SecretKey string `json:"secretKey"`
}

// IssueKeyRequest 是签发/重置请求。UserId 仅管理端为他人签发时使用
// （用户自助签发忽略该字段，恒为本人）。
type IssueKeyRequest struct {
	UserID        int64    `json:"userId"`
	Name          string   `json:"name"`
	ExpiresInDays *int     `json:"expiresInDays"`
	Scopes        []string `json:"scopes"`
	IPWhitelist   []string `json:"ipWhitelist"`
}

// Config 是开放 API 全局配置（单例表 bp_open_api_config）。
type Config struct {
	Enabled              bool `json:"enabled"`
	IPWhitelistMax       int  `json:"ipWhitelistMax"`
	KeyDefaultExpireDays int  `json:"keyDefaultExpireDays"`
	AdminKeyExpireDays   int  `json:"adminKeyExpireDays"`
	RateLimitNormal      int  `json:"rateLimitNormal"`
	RateLimitAdmin       int  `json:"rateLimitAdmin"`
}

// ConfigRequest 是全局配置更新请求（缺省字段保持原值）。
type ConfigRequest struct {
	Enabled              *bool `json:"enabled"`
	IPWhitelistMax       *int  `json:"ipWhitelistMax"`
	KeyDefaultExpireDays *int  `json:"keyDefaultExpireDays"`
	AdminKeyExpireDays   *int  `json:"adminKeyExpireDays"`
	RateLimitNormal      *int  `json:"rateLimitNormal"`
	RateLimitAdmin       *int  `json:"rateLimitAdmin"`
}

// ScopeOption 是签发表单的 scope 选项（默认勾选=角色派生结果）。
type ScopeOption struct {
	Code      string `json:"code"`
	Label     string `json:"label"`
	AdminOnly bool   `json:"adminOnly"`
	Granted   bool   `json:"granted"` // 持有账号当前角色是否具备（派生默认值）
}

// AccountInfo 是 GET /open-api/account 响应。
type AccountInfo struct {
	UserID         string       `json:"userId"`
	Username       string       `json:"username"`
	Status         int          `json:"status"`
	Balance        string       `json:"balance"`
	Points         int64        `json:"points"`
	OpenAPIEnabled bool         `json:"openApiEnabled"`
	KeyStatus      string       `json:"keyStatus"`
	PackageCount   PackageCount `json:"packageCount"`
}

// PackageCount 是账号有效套餐按类型计数。
type PackageCount struct {
	Global    int64 `json:"global"`
	Interface int64 `json:"interface"`
}

// AccountPackage 是 GET /open-api/account/packages 列表项。
type AccountPackage struct {
	Kind        string `json:"kind"`
	PackageName string `json:"packageName"`
	SpecName    string `json:"specName,omitempty"`
	InterfaceID string `json:"interfaceId,omitempty"`
	DailyLimit  int64  `json:"dailyLimit"`
	QPSLimit    int    `json:"qpsLimit"`
	StartAt     string `json:"startAt"`
	EndAt       string `json:"endAt"`
	DayUsed     int64  `json:"dayUsed"`
	Status      int    `json:"status"`
}

// AccountPackagePage 是分页响应。
type AccountPackagePage struct {
	Items []AccountPackage `json:"items"`
	Total int64            `json:"total"`
}

// CreateAPIKeyRequest 是 POST /open-api/api-keys 请求体（透传 gateway 域语义）。
type CreateAPIKeyRequest struct {
	Name         string   `json:"name"`
	KeyType      string   `json:"keyType"`
	ExpiresAt    *string  `json:"expiresAt"`
	InterfaceIDs []int64  `json:"interfaceIds"`
	IPWhitelist  []string `json:"ipWhitelist"`
}

// CreateOrderRequest 是 POST /open-api/orders 请求体。三类套餐按 Product 区分：
// GLOBAL（packageId）/ INTERFACE（specId）/ POINT（packageId）。
type CreateOrderRequest struct {
	Product    string `json:"product"`
	PackageID  int64  `json:"packageId"`
	SpecID     int64  `json:"specId"`
	PayChannel string `json:"payChannel"`
	TradeType  string `json:"tradeType"`
	OpenID     string `json:"openId"`
}

// RedeemCdkRequest 是 POST /open-api/redeem 请求体。
type RedeemCdkRequest struct {
	Code string `json:"code"`
}

// GenerateCodesRequest 是 POST /open-api/redeem-codes 请求体。
//
// 类型引用二选一：typeId（数据库自增 ID）或 typeCode（业务标识，如
// NEWBIE）。同时给出时 typeId 优先；外部脚本/插件在配置里写标识更稳，
// 因为标识不随重建数据库漂移。
type GenerateCodesRequest struct {
	TypeID       int64  `json:"typeId"`
	TypeCode     string `json:"typeCode"`
	Count        int    `json:"count"`
	PerUserLimit *int   `json:"perUserLimit"`
	PerIPLimit   *int   `json:"perIpLimit"`
	CampaignRef  string `json:"campaignRef"`
}
