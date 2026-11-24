package observability

type PageResult[T any] struct {
	Items      []T    `json:"items"`
	Total      int64  `json:"total"`
	NextCursor string `json:"nextCursor,omitempty"`
	// In opt-in fast mode total is -1, never a fabricated zero or truncated count.
	TotalOmitted bool `json:"totalOmitted,omitempty"`
}

type CallLogListParams struct {
	APIKeyID int64
	APICode  string
	// BillingOnly restricts the admin "billing records" view to real charged
	// calls whose interface currently has both billing switches enabled.
	BillingOnly      bool
	Billable         *int
	BillingStatus    string
	ClientIP         string
	ChargeType       string
	Cursor           string
	EndTime          string
	IncludeSensitive bool
	InterfaceID      int64
	Keyword          string
	MaxElapsedMs     int64
	MinElapsedMs     int64
	Page             int
	PageSize         int
	Region           string
	SkipTotal        bool
	StartTime        string
	Success          *int
	TraceID          string
	UserID           int64
	UserVisibleOnly  bool
}

type CallLogItem struct {
	APIKeyID          string `json:"apiKeyId,omitempty"`
	APIKeyPrefix      string `json:"apiKeyPrefix,omitempty"`
	APICode           string `json:"apiCode,omitempty"`
	Billable          bool   `json:"billable"`
	BillingRecordID   string `json:"billingRecordId,omitempty"`
	BillingSnapshot   string `json:"billingSnapshot,omitempty"`
	BillingStatus     string `json:"billingStatus"`
	ChargeAmount      string `json:"chargeAmount"`
	ChargeType        string `json:"chargeType"`
	ClientIP          string `json:"clientIp,omitempty"`
	ClientRegion      string `json:"clientRegion,omitempty"`
	CreateTime        string `json:"createTime"`
	ElapsedMs         int64  `json:"elapsedMs"`
	ErrorMessage      string `json:"errorMessage,omitempty"`
	ID                string `json:"id"`
	InterfaceID       string `json:"interfaceId"`
	InterfaceName     string `json:"interfaceName"`
	IPCity            string `json:"ipCity,omitempty"`
	IPCountry         string `json:"ipCountry,omitempty"`
	IPISP             string `json:"ipIsp,omitempty"`
	IPProvince        string `json:"ipProvince,omitempty"`
	Latitude          string `json:"latitude,omitempty"`
	Longitude         string `json:"longitude,omitempty"`
	PointPrice        int64  `json:"pointPrice"`
	PollingMode       string `json:"pollingMode,omitempty"`
	RealName          string `json:"realName,omitempty"`
	RegionProvider    string `json:"regionProvider,omitempty"`
	RequestMethod     string `json:"requestMethod"`
	RequestParams     string `json:"requestParams,omitempty"`
	RequestSize       int64  `json:"requestSize"`
	RequestValue      string `json:"requestValue,omitempty"`
	ResponseBody      string `json:"responseBody,omitempty"`
	ResponseSaved     int64  `json:"responseSavedBytes"`
	ResponseSize      int64  `json:"responseSize"`
	ResponseStatus    int    `json:"responseStatus"`
	ResponseTruncated bool   `json:"responseTruncated"`
	Success           bool   `json:"success"`
	TraceID           string `json:"traceId,omitempty"`
	UpstreamSwitched  bool   `json:"upstreamSwitched"`
	UpstreamURL       string `json:"upstreamUrl,omitempty"`
	UserID            string `json:"userId"`
	Username          string `json:"username"`
	UserVisible       bool   `json:"userVisible"`
}

// UserCallLogItem is the explicit allowlist returned by user-scoped call-log
// endpoints. Keep this DTO separate from CallLogItem so new administrator-only
// diagnostics cannot become user-visible by accident.
type UserCallLogItem struct {
	TraceID           string `json:"traceId,omitempty"`
	APICode           string `json:"apiCode,omitempty"`
	Billable          bool   `json:"billable"`
	BillingStatus     string `json:"billingStatus"`
	ChargeAmount      string `json:"chargeAmount"`
	ChargeType        string `json:"chargeType"`
	CreateTime        string `json:"createTime"`
	ElapsedMs         int64  `json:"elapsedMs"`
	ErrorMessage      string `json:"errorMessage,omitempty"`
	ID                string `json:"id"`
	InterfaceName     string `json:"interfaceName"`
	PointPrice        int64  `json:"pointPrice"`
	RequestMethod     string `json:"requestMethod"`
	RequestParams     string `json:"requestParams,omitempty"`
	RequestSize       int64  `json:"requestSize"`
	ResponseBody      string `json:"responseBody,omitempty"`
	ResponseSaved     int64  `json:"responseSavedBytes"`
	ResponseSize      int64  `json:"responseSize"`
	ResponseStatus    int    `json:"responseStatus"`
	ResponseTruncated bool   `json:"responseTruncated"`
	Success           bool   `json:"success"`
}

type CleanupConfig struct {
	AutoHideEnabled int    `json:"autoHideEnabled"`
	LastCleanupAt   string `json:"lastCleanupAt,omitempty"`
	RetentionDays   int    `json:"retentionDays"`
}

type CleanupConfigRequest struct {
	AutoHideEnabled *int `json:"autoHideEnabled"`
	RetentionDays   int  `json:"retentionDays"`
}

type DashboardOverview struct {
	APIEnabled         int64  `json:"apiEnabled"`
	APITotal           int64  `json:"apiTotal"`
	KeyEnabled         int64  `json:"keyEnabled"`
	KeyTotal           int64  `json:"keyTotal"`
	TodayBillableCalls int64  `json:"todayBillableCalls"`
	TodayCalls         int64  `json:"todayCalls"`
	TodayChargeAmount  string `json:"todayChargeAmount"`
	TodayFailedCalls   int64  `json:"todayFailedCalls"`
	TodayNewUsers      int64  `json:"todayNewUsers"`
	TodayRevenue       string `json:"todayRevenue"`
	TodaySuccessCalls  int64  `json:"todaySuccessCalls"`
	TodaySuccessRate   string `json:"todaySuccessRate"`
	TotalRevenue       string `json:"totalRevenue"`
	TotalUsers         int64  `json:"totalUsers"`
}

type TrendResult struct {
	BillableCalls []int64  `json:"billableCalls"`
	ChargeAmounts []string `json:"chargeAmounts"`
	FailedCalls   []int64  `json:"failedCalls"`
	Labels        []string `json:"labels"`
	SuccessCalls  []int64  `json:"successCalls"`
	Total         int64    `json:"total"`
	TotalCalls    []int64  `json:"totalCalls"`
}

type RankingItem struct {
	APICode   string `json:"apiCode,omitempty"`
	CallCount int64  `json:"callCount"`
	ID        string `json:"id,omitempty"`
	Name      string `json:"name"`
	Rate      string `json:"rate,omitempty"`
}
