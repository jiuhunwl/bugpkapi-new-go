package redeem

// 兑换码（CDK）域的类型定义。
//
// 与 packagepkg / payment 的对应关系：
//   - Principal 与各域保持一致（handler 从 JWT 解出后传入 service）；
//   - PageResult 泛型分页与 packagepkg 同形；
//   - 奖励发放镜像 payment applySettlement 口径（守卫式 UPDATE + 钱包流水）。

// PageResult 是列表分页的统一返回形。
type PageResult[T any] struct {
	Items []T   `json:"items"`
	Total int64 `json:"total"`
}

// Principal 是调用方身份（管理端 JWT 或用户端 JWT 解出）。
type Principal struct {
	ID           int64
	Username     string
	Roles        []string
	TokenVersion int
}

// 奖励类型（reward_type）全集。
const (
	RewardTypePoints   = 1 // 点数
	RewardTypeBalance  = 2 // 余额
	RewardTypeGlobal   = 3 // 全站套餐
	RewardTypeSpec     = 4 // 接口套餐规格
	RewardTypeCombined = 5 // 组合奖励（点数+余额）
)

// 兑换来源（bp_redeem_record.source）。
const (
	SourceConsole = 1 // 管理端页面
	SourceOpenAPI = 2 // 开放 API
)

// 码状态（bp_redeem_code.status）。
const (
	CodeStatusVoided   = 0 // 已作废
	CodeStatusUnused   = 1 // 未用
	CodeStatusConsumed = 2 // 已用
)

// 批量生成上限。
const (
	MaxGeneratePerBatch = 5000    // 单次生成上限
	MaxCodesPerType     = 100_000 // 单类型累计上限
	codeGroups          = 4       // 码分段数
	codeGroupLength     = 4       // 每段字符数
	maxGenerateAttempts = 12      // 撞唯一键后的重试轮数
	maxPerUserLimit     = 999     // 限 N 次上限
	maxPerIPLimit       = 999     // 每 IP 限 N 次上限
	maxExportRows       = 100_000 // 导出行数上限（=单类型累计上限）
)

// codeAlphabet 是 31 字符集：大写字母剔除 I/L/O，数字剔除 0/1，
// 31^16 ≈ 4.6×10^23，不可枚举；剔除形近字符降低人工转录错误。
const codeAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"

// CodeType 是兑换码类型模板（含列表聚合计数）。
type CodeType struct {
	ID             string `json:"id"`
	Code           string `json:"code"`
	Name           string `json:"name"`
	RewardType     int    `json:"rewardType"`
	RewardPoints   int64  `json:"rewardPoints"`
	RewardBalance  string `json:"rewardBalance"`
	RewardRefID    int64  `json:"rewardRefId"`
	RewardRefName  string `json:"rewardRefName,omitempty"`
	PerUserLimit   int    `json:"perUserLimit"`
	PerIPLimit     int    `json:"perIpLimit"`
	StartAt        string `json:"startAt"`
	EndAt          string `json:"endAt"`
	Status         int    `json:"status"`
	GeneratedCount int64  `json:"generatedCount"`
	RedeemedCount  int64  `json:"redeemedCount"`
	// Frozen 表示已生成过码：奖励与限领字段服务端拒绝编辑（M-2）。
	Frozen     bool   `json:"frozen"`
	CreateTime string `json:"createTime"`
	UpdateTime string `json:"updateTime"`
}

// SaveTypeRequest 是类型创建/更新请求。RewardBalance 接受字符串或数字，
// 与 packagepkg 的价格字段同口径（前端可能传 "10.00" 或 10）。
type SaveTypeRequest struct {
	Code          string `json:"code"`
	Name          string `json:"name"`
	RewardType    int    `json:"rewardType"`
	RewardPoints  int64  `json:"rewardPoints"`
	RewardBalance any    `json:"rewardBalance"`
	RewardRefID   int64  `json:"rewardRefId"`
	PerUserLimit  int    `json:"perUserLimit"`
	PerIPLimit    int    `json:"perIpLimit"`
	StartAt       string `json:"startAt"`
	EndAt         string `json:"endAt"`
	Status        *int   `json:"status"`
}

// GenerateCodesRequest 是批量生成请求。PerUserLimit / PerIPLimit 缺省沿用
// 类型模板值，活动级差异快照进批次（「生成后冻结」禁止改类型限领）。
type GenerateCodesRequest struct {
	Count        int    `json:"count"`
	PerUserLimit *int   `json:"perUserLimit"`
	PerIPLimit   *int   `json:"perIpLimit"`
	CampaignRef  string `json:"campaignRef"`
}

// RedeemCode 是码列表项。CodeMask 是脱敏展示（前 4 后 4），明文只出现在
// 导出流里（独立 redeem:code:export 权限）。
type RedeemCode struct {
	ID         string `json:"id"`
	TypeID     string `json:"typeId"`
	TypeName   string `json:"typeName"`
	CodeMask   string `json:"codeMask"`
	Status     int    `json:"status"`
	BatchID    string `json:"batchId,omitempty"`
	Source     int    `json:"source"`
	UsedBy     string `json:"usedBy,omitempty"`
	UsedAt     string `json:"usedAt,omitempty"`
	CreateTime string `json:"createTime"`
}

// RedeemRecord 是兑换记录（管理端视图带 username，用户端视图不带）。
type RedeemRecord struct {
	ID            string `json:"id"`
	UserID        string `json:"userId"`
	Username      string `json:"username,omitempty"`
	CodeMask      string `json:"codeMask"`
	TypeID        string `json:"typeId"`
	TypeName      string `json:"typeName"`
	RewardType    int    `json:"rewardType"`
	RewardPoints  int64  `json:"rewardPoints"`
	RewardBalance string `json:"rewardBalance"`
	RewardName    string `json:"rewardName,omitempty"`
	ValidDays     *int   `json:"validDays,omitempty"`
	Source        int    `json:"source"`
	CreateTime    string `json:"createTime"`
}

// RedeemRequest 是用户端兑换请求。
type RedeemRequest struct {
	Code string `json:"code"`
}

// RedeemResult 是兑换成功返回（含到账后的余额/点数，供前端即时展示）。
type RedeemResult struct {
	RewardType    int    `json:"rewardType"`
	RewardPoints  int64  `json:"rewardPoints"`
	RewardBalance string `json:"rewardBalance"`
	RewardName    string `json:"rewardName,omitempty"`
	ValidDays     *int   `json:"validDays,omitempty"`
	BalanceAfter  string `json:"balanceAfter"`
	PointsAfter   int64  `json:"pointsAfter"`
}

// ListParams 是码/记录列表的查询参数。
type ListParams struct {
	Keyword  string
	TypeID   int64
	Status   *int
	Source   *int
	Page     int
	PageSize int
}

// typeCounts 是类型列表的聚合计数。
type typeCounts struct {
	generated int64
	redeemed  int64
}

// packageRef 是套餐引用的解析结果（类型保存校验与发放快照共用）。
type packageRef struct {
	Name        string
	ValidDays   int
	DailyLimit  int64
	QPSLimit    int
	InterfaceID int64
	PackageID   int64
	SpecName    string
}
