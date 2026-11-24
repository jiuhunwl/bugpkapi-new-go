package observability

// 系统操作日志（审计日志）的类型定义。
//
// 与 bp_interface_call_log（网关调用日志）严格区分：
//   - 调用日志记录「API 被谁调用」，面向计费与流量分析；
//   - 操作日志记录「管理员/用户对系统做了什么」，面向审计与安全追溯。
//
// 两者的写入路径、保留策略、访问权限都不同，因此是独立的两张表。

// OperationLogModule 是操作日志的模块枚举。模块决定侧栏筛选分组，
// 取值必须是稳定短码（写入后不改），中文名由前端 i18n 映射。
type OperationLogModule string

const (
	OperationLogModuleAuth       OperationLogModule = "auth"       // 登录、登出、注册、改密
	OperationLogModuleUser       OperationLogModule = "user"       // 用户增删改、状态变更
	OperationLogModuleInterface  OperationLogModule = "interface"  // 接口配置
	OperationLogModuleKey        OperationLogModule = "key"        // API Key
	OperationLogModulePackage    OperationLogModule = "package"    // 套餐
	OperationLogModulePayment    OperationLogModule = "payment"    // 支付与订单
	OperationLogModuleContent    OperationLogModule = "content"    // 内容运营
	OperationLogModuleTicket     OperationLogModule = "ticket"     // 工单
	OperationLogModuleRisk       OperationLogModule = "risk"       // 风控
	OperationLogModuleSystem     OperationLogModule = "system"     // 系统设置
	OperationLogModulePermission OperationLogModule = "permission" // 角色与权限
	OperationLogModuleRedeem     OperationLogModule = "redeem"     // 兑换码
	OperationLogModuleOpenAPI    OperationLogModule = "openapi"    // 系统开放 API
)

// OperationLogAction 是动作码。保持动宾结构，便于按动作聚合。
type OperationLogAction string

const (
	OperationLogActionLogin       OperationLogAction = "login"
	OperationLogActionLogout      OperationLogAction = "logout"
	OperationLogActionRegister    OperationLogAction = "register"
	OperationLogActionPassword    OperationLogAction = "password"
	OperationLogActionCreate      OperationLogAction = "create"
	OperationLogActionUpdate      OperationLogAction = "update"
	OperationLogActionDelete      OperationLogAction = "delete"
	OperationLogActionStatus      OperationLogAction = "status"
	OperationLogActionExport      OperationLogAction = "export"
	OperationLogActionReview      OperationLogAction = "review"
	OperationLogActionAdjustAsset OperationLogAction = "adjust-asset"
	OperationLogActionConfig      OperationLogAction = "config"
)

// OperationLogRecord 是写入审计表的一条记录。字段与 bp_operation_log 一一对应。
//
// 写入方（审计中间件 / 领域服务）只填业务语义字段，client_ip_hash 由
// 仓储层统一计算，避免每个调用点各自实现哈希。
type OperationLogRecord struct {
	// Event-time classification. Empty is legacy/unclassified, never an inferred role.
	ActorKind    string
	UserID       int64
	Username     string
	Module       string
	Action       string
	Summary      string
	Method       string
	Path         string
	TargetType   string
	TargetID     string
	Status       bool
	ErrorMessage string
	StatusCode   int
	ClientIP     string
	Country      string
	Province     string
	City         string
	UserAgent    string
	ElapsedMS    int
	Detail       string
}

// OperationLogItem 是列表返回的一条记录。IP 明文只在管理员查询时返回，
// 并对访问者本身做权限校验（见 Service.ListOperationLogs）。
type OperationLogItem struct {
	ID           string `json:"id"`
	UserID       string `json:"userId"`
	Username     string `json:"username"`
	Module       string `json:"module"`
	Action       string `json:"action"`
	Summary      string `json:"summary"`
	Method       string `json:"method"`
	Path         string `json:"path"`
	TargetType   string `json:"targetType,omitempty"`
	TargetID     string `json:"targetId,omitempty"`
	Status       bool   `json:"status"`
	ErrorMessage string `json:"errorMessage,omitempty"`
	StatusCode   int    `json:"statusCode"`
	ClientIP     string `json:"clientIp,omitempty"`
	Country      string `json:"country,omitempty"`
	Province     string `json:"province,omitempty"`
	City         string `json:"city,omitempty"`
	UserAgent    string `json:"userAgent,omitempty"`
	ElapsedMS    int    `json:"elapsedMs"`
	Detail       string `json:"detail,omitempty"`
	CreatedAt    string `json:"createdAt"`
}

// OperationLogListQuery requires a service-owned scope; a zero scope fails closed.
type OperationLogListQuery struct {
	// Scope is service-owned, never bound from request parameters.
	Scope     operationLogScope
	UserID    int64
	Username  string
	Module    string
	Action    string
	Keyword   string
	ClientIP  string
	Status    *bool
	StartTime string
	EndTime   string
	Page      int
	PageSize  int
}

// OperationLogStats aggregates classified admin events in a bounded detail window.
type OperationLogStats struct {
	TotalCount    int64                    `json:"totalCount"`
	TodayCount    int64                    `json:"todayCount"`
	FailureCount  int64                    `json:"failureCount"`
	LoginCount    int64                    `json:"loginCount"`
	ActiveUsers   int64                    `json:"activeUsers"`
	ModuleTotals  []OperationLogModuleStat `json:"moduleTotals"`
	RecentActions []OperationLogActionStat `json:"recentActions"`
}

type OperationLogModuleStat struct {
	Module string `json:"module"`
	Count  int64  `json:"count"`
}

type OperationLogActionStat struct {
	Action string `json:"action"`
	Module string `json:"module"`
	Count  int64  `json:"count"`
}

// OperationLogTrendPoint 是操作日志按天趋势的一个点，供运营告警复用。
type OperationLogTrendPoint struct {
	Date         string `json:"date"`
	TotalCount   int64  `json:"totalCount"`
	FailureCount int64  `json:"failureCount"`
	LoginCount   int64  `json:"loginCount"`
}

type operationLogScope uint8

const (
	operationLogScopeAdmin operationLogScope = iota + 1
	operationLogScopePersonal
)
