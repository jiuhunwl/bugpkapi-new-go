package riskcontrol

type RiskQuery struct {
	Limit         int
	WindowMinutes int
}

type BanAccountRequest struct {
	BanUntil string `json:"banUntil"`
	Mode     string `json:"mode"`
	Reason   string `json:"reason"`
	UserID   int64  `json:"userId"`
}

type Overview struct {
	DistributedEvents    int                 `json:"distributedEvents"`
	EarthPointCount      int64               `json:"earthPointCount"`
	Events               []RiskEvent         `json:"events"`
	FailedRequests       int64               `json:"failedRequests"`
	InvalidKeyAttempts   int64               `json:"invalidKeyAttempts"`
	InvalidKeyBlocked    int64               `json:"invalidKeyBlocked"`
	Policy               DetectionPolicy     `json:"policy"`
	ProxyPoolEvents      int                 `json:"proxyPoolEvents"`
	RiskScore            int                 `json:"riskScore"`
	RiskyRequests        int64               `json:"riskyRequests"`
	SingleIPEvents       int                 `json:"singleIpEvents"`
	TopAttackIP          string              `json:"topAttackIp,omitempty"`
	TopAttackParam       string              `json:"topAttackParam,omitempty"`
	TotalRequests        int64               `json:"totalRequests"`
	TopFailedAccounts    []FailedAccountRank `json:"topFailedAccounts"`
	TopFailedIPs         []FailedIPRank      `json:"topFailedIps"`
	Trend                []TrendPoint        `json:"trend"`
	UniqueIPs            int64               `json:"uniqueIps"`
	UniqueParamSignature int64               `json:"uniqueParamSignatures"`
	WindowMinutes        int                 `json:"windowMinutes"`
}

// FailedIPRank is an administrator-only aggregate of unsuccessful calls for
// one client IP in the selected window. It contains operational evidence but
// never exposes request payloads or API secrets.
type FailedIPRank struct {
	APICode              string `json:"apiCode,omitempty"`
	ClientIP             string `json:"clientIp"`
	FailedCalls          int64  `json:"failedCalls"`
	InterfaceFailedCalls int64  `json:"interfaceFailedCalls"`
	InterfaceID          string `json:"interfaceId,omitempty"`
	InterfaceName        string `json:"interfaceName,omitempty"`
	LastSeen             string `json:"lastSeen"`
	Region               string `json:"region,omitempty"`
	RiskHits             int64  `json:"riskHits"`
	TotalCalls           int64  `json:"totalCalls"`
	UniqueUsers          int64  `json:"uniqueUsers"`
}

// FailedAccountRank is an administrator-only aggregate keyed by the stable
// account id. Username/display name are looked up from bp_user at query time
// so deleted or renamed accounts remain identifiable without leaking secrets.
type FailedAccountRank struct {
	APICode              string `json:"apiCode,omitempty"`
	DisplayName          string `json:"displayName,omitempty"`
	FailedCalls          int64  `json:"failedCalls"`
	InterfaceFailedCalls int64  `json:"interfaceFailedCalls"`
	InterfaceID          string `json:"interfaceId,omitempty"`
	InterfaceName        string `json:"interfaceName,omitempty"`
	LastSeen             string `json:"lastSeen"`
	RiskHits             int64  `json:"riskHits"`
	Status               int    `json:"status"`
	TotalCalls           int64  `json:"totalCalls"`
	UniqueIPs            int64  `json:"uniqueIps"`
	UserID               string `json:"userId"`
	Username             string `json:"username,omitempty"`
}

type DetectionPolicy struct {
	BurstWindowMinutes  int `json:"burstWindowMinutes"`
	DistributedIPs      int `json:"distributedIps"`
	DistributedRequests int `json:"distributedRequests"`
	ProxyPoolIPs        int `json:"proxyPoolIps"`
	ProxyPoolRequests   int `json:"proxyPoolRequests"`
	SingleIPRequests    int `json:"singleIpRequests"`
}

type RiskEvent struct {
	Accounts        []RiskAccount `json:"accounts,omitempty"`
	APICode         string        `json:"apiCode,omitempty"`
	AvgElapsedMs    int64         `json:"avgElapsedMs"`
	APIKeys         []string      `json:"apiKeys,omitempty"`
	ClientIP        string        `json:"clientIp,omitempty"`
	EventType       string        `json:"eventType"`
	FailedCount     int64         `json:"failedCount"`
	FirstSeen       string        `json:"firstSeen"`
	InterfaceID     string        `json:"interfaceId,omitempty"`
	InterfaceName   string        `json:"interfaceName,omitempty"`
	LastSeen        string        `json:"lastSeen"`
	ParamSignature  string        `json:"paramSignature"`
	Reason          string        `json:"reason"`
	Region          string        `json:"region,omitempty"`
	RequestIPs      []string      `json:"requestIps,omitempty"`
	RequestMethods  []string      `json:"requestMethods,omitempty"`
	RequestCount    int64         `json:"requestCount"`
	RequestSample   string        `json:"requestSample,omitempty"`
	RiskScore       int           `json:"riskScore"`
	Severity        string        `json:"severity"`
	SuccessCount    int64         `json:"successCount"`
	UniqueIPCount   int64         `json:"uniqueIpCount"`
	UniqueKeyCount  int64         `json:"uniqueKeyCount"`
	UniqueUserCount int64         `json:"uniqueUserCount"`
}

// RiskAccount is the safe, administrator-visible identity evidence attached
// to a risk event. It intentionally contains only the stable user id and
// login name; secrets and profile data never leave the risk-control query.
type RiskAccount struct {
	ID       string `json:"id"`
	Username string `json:"username,omitempty"`
}

type TrendPoint struct {
	Label       string `json:"label"`
	RiskyEvents int64  `json:"riskyEvents"`
	TotalCalls  int64  `json:"totalCalls"`
}

type EarthOverview struct {
	ActivePoints  int          `json:"activePoints"`
	Events        []RiskEvent  `json:"events"`
	Points        []EarthPoint `json:"points"`
	TotalCalls    int64        `json:"totalCalls"`
	TotalRiskHits int64        `json:"totalRiskHits"`
	WindowMinutes int          `json:"windowMinutes"`
}

type EarthPoint struct {
	CallCount    int64  `json:"callCount"`
	City         string `json:"city,omitempty"`
	Country      string `json:"country,omitempty"`
	FailedCalls  int64  `json:"failedCalls"`
	ISP          string `json:"isp,omitempty"`
	Latitude     string `json:"latitude"`
	Longitude    string `json:"longitude"`
	Province     string `json:"province,omitempty"`
	Region       string `json:"region"`
	RiskHits     int64  `json:"riskHits"`
	RiskLevel    string `json:"riskLevel"`
	SuccessCalls int64  `json:"successCalls"`
	UniqueIPs    int64  `json:"uniqueIps"`
}

// HomeSnapshot intentionally contains no client IP, API key, user identity, or
// request sample. It is safe to merge into the public home overview.
type HomeSnapshot struct {
	EventCount    int             `json:"eventCount"`
	Points        []HomeRiskPoint `json:"points"`
	RiskScore     int             `json:"riskScore"`
	RiskyRequests int64           `json:"riskyRequests"`
}

type HomeRiskPoint struct {
	Latitude  string `json:"latitude"`
	Longitude string `json:"longitude"`
	Region    string `json:"region"`
	RiskHits  int64  `json:"riskHits"`
}
