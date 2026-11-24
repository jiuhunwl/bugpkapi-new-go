package observability

import (
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"bugpk-api/internal/domain/rbac"
	"bugpk-api/internal/pkg/businessclock"
	apperrors "bugpk-api/internal/pkg/errors"
	"bugpk-api/internal/pkg/masking"
	"bugpk-api/internal/pkg/pagination"

	"github.com/cloudwego/hertz/pkg/common/hlog"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

const (
	maxCallLogExportRows   = 50_000
	callLogExportBatchSize = 500
	// callLogExportMaxDuration 是整个导出流的总时长上限：约束 100 批 × 15s
	// 的最坏排队时长，防止慢客户端/慢查询把导出会话无限拉长。下游每批的
	// runQuery 超时取 min(本上限, 单批超时)。
	callLogExportMaxDuration = 10 * time.Minute
)

var callLogExportHeader = []string{"ID", "Time", "Interface", "API Code", "User", "API Key", "Method", "Status", "Success", "Billable", "Charge Type", "Charge Amount", "Point Price", "Elapsed Ms", "Region", "Client IP", "Trace ID", "Error", "Request Params"}

var networkAddressPattern = regexp.MustCompile(`(?i)(?:\d{1,3}\.){3}\d{1,3}(?::\d+)?`)

type Service struct {
	repo           *Repository
	authz          *rbac.Service
	dashboardCache *dashboardReadCache
	fuzzy          *fuzzyRateLimiter
	// region/regionResolver 由 SetIPRegionResolver 注入；未注入时审计记录的
	// 地区列保持为空，前端显示「—」。
	region         *regionBackfiller
	regionResolver OperationLogRegionResolver
}

func NewService(repo *Repository, authz *rbac.Service) *Service {
	return &Service{
		repo:           repo,
		authz:          authz,
		dashboardCache: newDashboardReadCache(dashboardReadCacheTTL),
		fuzzy:          newFuzzyRateLimiter(defaultFuzzyWindow, defaultFuzzyBurst),
	}
}

// allowFuzzySearch 对模糊兜底搜索限频；等值筛选（traceId 等）不受影响。
func (s *Service) allowFuzzySearch(principal rbac.Principal, params CallLogListParams) error {
	if strings.TrimSpace(params.Keyword) == "" && strings.TrimSpace(params.Region) == "" {
		return nil
	}
	if s.fuzzy.allow(principal.ID, time.Now()) {
		return nil
	}
	return apperrors.New(consts.StatusTooManyRequests, "Too many fuzzy search requests; slow down or use exact filters")
}

func (s *Service) Authenticate(ctx context.Context, token string) (rbac.Principal, error) {
	return s.authz.Authenticate(ctx, token)
}

func (s *Service) AdminCallLogs(ctx context.Context, principal rbac.Principal, params CallLogListParams) (PageResult[CallLogItem], error) {
	if err := s.authz.Require(ctx, principal, "log:call:list"); err != nil {
		return PageResult[CallLogItem]{}, err
	}
	if err := s.allowFuzzySearch(principal, params); err != nil {
		return PageResult[CallLogItem]{}, err
	}
	if params.IncludeSensitive && !canViewSensitiveCallLogs(principal) {
		return PageResult[CallLogItem]{}, apperrors.New(consts.StatusForbidden, "Only administrators can view unmasked sensitive data")
	}
	result, err := s.repo.ListCallLogs(ctx, params, true, principal.ID)
	if err != nil {
		return result, err
	}
	// Upstream addresses are routing secrets and are never restored by the
	// explicit unmasked switch. Keep the switch available for other audited
	// fields (full client IP/response data), but enforce provider endpoint
	// removal without erasing caller-supplied business URLs. This also
	// protects historical rows that predate the
	// persistence redaction change.
	for index := range result.Items {
		sanitizeCallLogEndpointFields(&result.Items[index])
	}
	if params.IncludeSensitive {
		return result, nil
	}
	maskCallLogPage(&result)
	return result, nil
}

// StreamExportCallLogs 以 keyset 分批查询并流式写 CSV，内存占用被单批行数约束。
func (s *Service) StreamExportCallLogs(ctx context.Context, principal rbac.Principal, params CallLogListParams, out io.Writer) error {
	if err := s.authz.Require(ctx, principal, "log:call:export"); err != nil {
		return err
	}
	// 导出与列表共用模糊搜索限频，避免导出成为绕过限频的唯一出口。
	if err := s.allowFuzzySearch(principal, params); err != nil {
		return err
	}
	// 总时长上限覆盖整个分批循环；拒绝在守卫阶段消耗导出预算。
	ctx, cancel := context.WithTimeout(ctx, callLogExportMaxDuration)
	defer cancel()
	params.IncludeSensitive = false
	writer := csv.NewWriter(out)
	started := false
	cursor := ""
	total := 0
	for {
		items, next, err := s.repo.ExportCallLogBatch(ctx, params, true, principal.ID, callLogExportBatchSize, cursor)
		if err != nil {
			return err
		}
		// Keep the response uncommitted until the first query succeeds so
		// validation/database failures can still return a normal JSON error.
		if !started {
			if _, err := out.Write([]byte{0xEF, 0xBB, 0xBF}); err != nil {
				return err
			}
			if err := writer.Write(callLogExportHeader); err != nil {
				return err
			}
			started = true
		}
		for index := range items {
			writeCallLogCSVRow(writer, maskCallLogItem(items[index]))
		}
		writer.Flush()
		if err := writer.Error(); err != nil {
			return err
		}
		total += len(items)
		if next == "" {
			return nil
		}
		if total >= maxCallLogExportRows {
			if err := writer.Write([]string{"NOTE", fmt.Sprintf("export truncated at %d rows; narrow the filters", maxCallLogExportRows)}); err != nil {
				return err
			}
			writer.Flush()
			return writer.Error()
		}
		cursor = next
	}
}

func writeCallLogCSVRow(writer *csv.Writer, item CallLogItem) {
	row := []string{
		item.ID, item.CreateTime, item.InterfaceName, item.APICode, item.Username,
		item.APIKeyPrefix, item.RequestMethod, strconv.Itoa(item.ResponseStatus), strconv.FormatBool(item.Success),
		strconv.FormatBool(item.Billable), item.ChargeType, item.ChargeAmount, strconv.FormatInt(item.PointPrice, 10),
		strconv.FormatInt(item.ElapsedMs, 10), item.ClientRegion, item.ClientIP, item.TraceID, item.ErrorMessage, item.RequestParams,
	}
	for index := range row {
		row[index] = safeCSVCell(row[index])
	}
	_ = writer.Write(row)
}

func safeCSVCell(value string) string {
	trimmed := strings.TrimLeft(value, " \t\r\n")
	if trimmed == "" {
		return value
	}
	switch trimmed[0] {
	case '=', '+', '-', '@':
		return "'" + value
	}
	return value
}

func (s *Service) UserCallLogs(ctx context.Context, principal rbac.Principal, params CallLogListParams) (PageResult[UserCallLogItem], error) {
	if err := s.allowFuzzySearch(principal, params); err != nil {
		return PageResult[UserCallLogItem]{}, err
	}
	if _, err := s.repo.ApplyUserAutoHide(ctx, principal.ID); err != nil {
		return PageResult[UserCallLogItem]{}, err
	}
	// 请求 ID 可用于用户反馈；其查询仍受 principal.ID 和 UserVisibleOnly 限制。
	// 其余内部诊断字段不对用户开放。
	params.ClientIP, params.APICode = "", ""
	params.UserVisibleOnly = true
	params.IncludeSensitive = false
	result, err := s.repo.ListCallLogs(ctx, params, false, principal.ID)
	if err != nil {
		return PageResult[UserCallLogItem]{}, err
	}
	return userCallLogPage(result), nil
}

func (s *Service) AdminCallLogDetail(ctx context.Context, principal rbac.Principal, id int64, includeSensitive bool) (CallLogItem, error) {
	if err := s.authz.Require(ctx, principal, "log:call:detail"); err != nil {
		return CallLogItem{}, err
	}
	if includeSensitive && !canViewSensitiveCallLogs(principal) {
		return CallLogItem{}, apperrors.New(consts.StatusForbidden, "Only administrators can view unmasked sensitive data")
	}
	item, err := mapNotFoundItem(s.repo.CallLogDetail(ctx, id, principal.ID, true))
	if err != nil {
		return item, err
	}
	// Never expose a recoverable upstream address, even for administrators who
	// request other sensitive call-log fields. Apply the same endpoint scrub to
	// request/response payloads because historical rows can contain a nested
	// upstream URL even when the dedicated column is already redacted.
	sanitizeCallLogEndpointFields(&item)
	if includeSensitive {
		return item, nil
	}
	return maskCallLogItem(item), nil
}

func (s *Service) UserCallLogDetail(ctx context.Context, principal rbac.Principal, id int64) (UserCallLogItem, error) {
	item, err := mapNotFoundItem(s.repo.CallLogDetail(ctx, id, principal.ID, false))
	if err != nil {
		return UserCallLogItem{}, err
	}
	return toUserCallLogItem(item), nil
}

func (s *Service) HideUserLog(ctx context.Context, principal rbac.Principal, id int64) error {
	return mapNotFound(s.repo.HideUserLog(ctx, id, principal.ID))
}

func (s *Service) CleanupLogs(ctx context.Context, principal rbac.Principal, before string) (map[string]any, error) {
	if err := s.authz.Require(ctx, principal, "log:call:cleanup"); err != nil {
		return nil, err
	}
	beforeValue, err := parseCleanupBefore(before, time.Now())
	if err != nil {
		return nil, err
	}
	affected, hasMore, err := s.repo.CleanupLogs(ctx, beforeValue)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"batchLimit": callLogCleanupBatchSize * callLogCleanupMaxBatches,
		"deleted":    affected,
		"hasMore":    hasMore,
	}, nil
}

func parseCleanupBefore(value string, now time.Time) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", apperrors.New(consts.StatusBadRequest, "before不能为空")
	}
	var parsed time.Time
	var err error
	for _, layout := range []string{"2006-01-02 15:04:05", time.RFC3339} {
		parsed, err = time.ParseInLocation(layout, value, businessclock.Location())
		if err == nil {
			break
		}
	}
	if err != nil {
		return "", apperrors.New(consts.StatusBadRequest, "before必须是YYYY-MM-DD HH:mm:ss或RFC3339时间")
	}
	if !parsed.Before(now) {
		return "", apperrors.New(consts.StatusBadRequest, "before必须早于当前时间")
	}
	return parsed.In(businessclock.Location()).Format("2006-01-02 15:04:05"), nil
}

func defaultCleanupCutoff(now time.Time) string {
	return now.In(businessclock.Location()).AddDate(0, 0, -1).Format("2006-01-02") + " 23:59:59"
}

func (s *Service) CleanupConfig(ctx context.Context, principal rbac.Principal) (CleanupConfig, error) {
	return s.repo.CleanupConfig(ctx, principal.ID)
}

func (s *Service) SaveCleanupConfig(ctx context.Context, principal rbac.Principal, req CleanupConfigRequest) error {
	enabled := 0
	if req.AutoHideEnabled != nil {
		enabled = *req.AutoHideEnabled
	}
	if enabled != 0 && enabled != 1 {
		return apperrors.New(consts.StatusBadRequest, "自动清理状态不合法")
	}
	if req.RetentionDays < 1 || req.RetentionDays > 3650 {
		return apperrors.New(consts.StatusBadRequest, "日志保留天数不合法")
	}
	return s.repo.SaveCleanupConfig(ctx, principal.ID, CleanupConfig{RetentionDays: req.RetentionDays, AutoHideEnabled: enabled})
}

func (s *Service) Overview(ctx context.Context, principal rbac.Principal) (DashboardOverview, error) {
	if err := s.authz.Require(ctx, principal, "dashboard:admin:view"); err != nil {
		return DashboardOverview{}, err
	}
	return dashboardCached(s.dashboardCache, ctx, "overview", func() (DashboardOverview, error) {
		return s.repo.Overview(ctx)
	})
}

func (s *Service) CallTrend(ctx context.Context, principal rbac.Principal, days int) (TrendResult, error) {
	if err := s.authz.Require(ctx, principal, "dashboard:admin:view"); err != nil {
		return TrendResult{}, err
	}
	if days <= 0 || days > 90 {
		days = 7
	}
	return dashboardCached(s.dashboardCache, ctx, fmt.Sprintf("trend:%d", days), func() (TrendResult, error) {
		return s.repo.CallTrend(ctx, days)
	})
}

func (s *Service) RegionRanking(ctx context.Context, principal rbac.Principal, limit int) ([]RankingItem, error) {
	if err := s.authz.Require(ctx, principal, "dashboard:admin:view"); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 100 {
		limit = 10
	}
	return dashboardCached(s.dashboardCache, ctx, fmt.Sprintf("region:%d", limit), func() ([]RankingItem, error) {
		return s.repo.RegionRanking(ctx, limit)
	})
}

func (s *Service) HotAPIs(ctx context.Context, principal rbac.Principal, limit int) ([]RankingItem, error) {
	if err := s.authz.Require(ctx, principal, "dashboard:admin:view"); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 100 {
		limit = 10
	}
	return dashboardCached(s.dashboardCache, ctx, fmt.Sprintf("hot:%d", limit), func() ([]RankingItem, error) {
		return s.repo.HotAPIs(ctx, limit)
	})
}

func mapNotFoundItem(item CallLogItem, err error) (CallLogItem, error) {
	if err == nil {
		return item, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return CallLogItem{}, apperrors.New(consts.StatusNotFound, "调用日志不存在")
	}
	return CallLogItem{}, err
}

func mapNotFound(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return apperrors.New(consts.StatusNotFound, "调用日志不存在")
	}
	return err
}

func maskCallLogPage(result *PageResult[CallLogItem]) {
	for index := range result.Items {
		result.Items[index] = maskCallLogItem(result.Items[index])
	}
}

func maskCallLogItem(item CallLogItem) CallLogItem {
	item.ClientIP = masking.IP(item.ClientIP)
	item.ErrorMessage = masking.ErrorText(item.ErrorMessage)
	item.RequestParams = masking.SensitiveJSONTextWithURLs(item.RequestParams)
	item.RequestValue = maskedRequestValueForDisplay(item.RequestValue)
	// 响应体是调用方本来就收到的业务数据：保留公开业务 URL（解析出的直链等），
	// 只脱敏凭据/邮箱/手机号、URL 查询敏感值与内网地址。上游路由地址由
	// upstream_url 列单独保护，诊断字段 error_message 仍走 ErrorText。
	item.ResponseBody = masking.ResponseJSONText(item.ResponseBody)
	item.UpstreamURL = masking.UpstreamReference(item.UpstreamURL)
	return item
}

// maskedRequestValueForDisplay handles caller-supplied values, not the upstream
// request URL. Preserve business JSON/URLs while masking credentials. Legacy
// non-structured diagnostics retain the stricter endpoint-redaction fallback.
func maskedRequestValueForDisplay(value string) string {
	trimmed := strings.TrimSpace(value)
	if json.Valid([]byte(trimmed)) {
		return masking.SensitiveJSONTextWithURLs(trimmed)
	}
	parsed, err := url.Parse(trimmed)
	if err == nil && parsed.Host != "" && (strings.EqualFold(parsed.Scheme, "http") || strings.EqualFold(parsed.Scheme, "https")) {
		return masking.SensitiveJSONTextWithURLs(trimmed)
	}
	return masking.LogText(trimmed)
}

// sanitizeCallLogEndpointFields protects provider configuration and diagnostics,
// including historical rows and the admin raw view. RequestParams/RequestValue
// originate from caller input and ResponseBody is caller-received business data:
// mask credentials and internal hosts, not every business URL. UpstreamURL and
// ErrorMessage keep the strict endpoint redaction.
func sanitizeCallLogEndpointFields(item *CallLogItem) {
	if item == nil {
		return
	}
	item.RequestParams = masking.SensitiveJSONTextWithURLs(item.RequestParams)
	item.RequestValue = maskedRequestValueForDisplay(item.RequestValue)
	item.ResponseBody = masking.ResponseJSONText(item.ResponseBody)
	item.ErrorMessage = masking.ErrorText(item.ErrorMessage)
	item.UpstreamURL = masking.UpstreamReference(item.UpstreamURL)
}

func userCallLogPage(result PageResult[CallLogItem]) PageResult[UserCallLogItem] {
	items := make([]UserCallLogItem, len(result.Items))
	for index := range result.Items {
		items[index] = toUserCallLogItem(result.Items[index])
	}
	return PageResult[UserCallLogItem]{Items: items, Total: result.Total, NextCursor: result.NextCursor, TotalOmitted: result.TotalOmitted}
}

func toUserCallLogItem(item CallLogItem) UserCallLogItem {
	return UserCallLogItem{
		TraceID:           item.TraceID,
		APICode:           item.APICode,
		Billable:          item.Billable,
		BillingStatus:     item.BillingStatus,
		ChargeAmount:      item.ChargeAmount,
		ChargeType:        item.ChargeType,
		CreateTime:        item.CreateTime,
		ElapsedMs:         item.ElapsedMs,
		ErrorMessage:      sanitizeUserErrorMessage(masking.ErrorText(item.ErrorMessage)),
		ID:                item.ID,
		InterfaceName:     item.InterfaceName,
		PointPrice:        item.PointPrice,
		RequestMethod:     item.RequestMethod,
		RequestParams:     masking.UserVisibleJSONText(masking.SensitiveJSONTextWithURLs(item.RequestParams)),
		RequestSize:       item.RequestSize,
		ResponseBody:      masking.UserVisibleJSONText(item.ResponseBody),
		ResponseSaved:     item.ResponseSaved,
		ResponseSize:      item.ResponseSize,
		ResponseStatus:    item.ResponseStatus,
		ResponseTruncated: item.ResponseTruncated,
		Success:           item.Success,
	}
}

func sanitizeUserErrorMessage(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	lower := strings.ToLower(value)
	internalMarkers := []string{
		"http://", "https://", "dial tcp", "connectex", "connection refused",
		"no such host", "i/o timeout", "tls handshake", "server misbehaving",
		"network is unreachable", "connection reset", "broken pipe",
	}
	for _, marker := range internalMarkers {
		if strings.Contains(lower, marker) {
			return "上游服务暂时不可用"
		}
	}
	if networkAddressPattern.MatchString(value) {
		return "上游服务暂时不可用"
	}
	return value
}

func hasRole(roles []string, target string) bool {
	for _, role := range roles {
		if role == target {
			return true
		}
	}
	return false
}

func canViewSensitiveCallLogs(principal rbac.Principal) bool {
	return hasRole(principal.Roles, "admin")
}

// ---------------------------------------------------------------------------
// 系统操作日志（审计）
// ---------------------------------------------------------------------------

// OperationLogListParams 是控制台操作日志列表的查询参数（HTTP 层 -> service）。
type OperationLogListParams struct {
	Action   string
	ClientIP string
	Keyword  string
	Module   string
	Status   *bool
	Username string
	UserID   int64
	Start    string
	End      string
	Page     int
	PageSize int
}

const operationLogPermission = "system:operation-log:list"

// OperationLogPage 是操作日志列表的分页结果。
//
// 不复用 PageResult：那个结构是为调用日志的 keyset 游标分页设计的（带 nextCursor），
// 审计场景需要返回 page/pageSize 让前端渲染页码器，两者语义不同。
type OperationLogPage struct {
	Items    []OperationLogItem `json:"items"`
	Total    int64              `json:"total"`
	Page     int                `json:"page"`
	PageSize int                `json:"pageSize"`
}

// AdminOperationLogs 返回操作日志分页列表。
//
// 权限沿用菜单 auth_code（system:operation-log:list），admin 角色按既有约定绕过校验。
// 客户端 IP 明文只对 admin 可见：其他角色看到的是脱敏后的网段，防止越权采集办公网出口 IP。
func (s *Service) AdminOperationLogs(ctx context.Context, principal rbac.Principal, params OperationLogListParams) (OperationLogPage, error) {
	if principal.ID <= 0 {
		return OperationLogPage{}, apperrors.New(consts.StatusUnauthorized, "Unauthorized Exception")
	}
	if err := s.authz.Require(ctx, principal, operationLogPermission); err != nil {
		return OperationLogPage{}, err
	}
	return s.operationLogs(ctx, principal, params, operationLogScopeAdmin)
}

// MyOperationLogs cannot be widened by userId/username supplied by the client.
func (s *Service) MyOperationLogs(ctx context.Context, principal rbac.Principal, params OperationLogListParams) (OperationLogPage, error) {
	if principal.ID <= 0 {
		return OperationLogPage{}, apperrors.New(consts.StatusUnauthorized, "Unauthorized Exception")
	}
	params.UserID, params.Username = principal.ID, ""
	return s.operationLogs(ctx, principal, params, operationLogScopePersonal)
}

func (s *Service) operationLogs(ctx context.Context, principal rbac.Principal, params OperationLogListParams, scope operationLogScope) (OperationLogPage, error) {
	items, total, err := s.repo.ListOperationLogs(ctx, OperationLogListQuery{
		Scope:     scope,
		UserID:    params.UserID,
		Username:  params.Username,
		Module:    params.Module,
		Action:    params.Action,
		Keyword:   params.Keyword,
		ClientIP:  params.ClientIP,
		Status:    params.Status,
		StartTime: params.Start,
		EndTime:   params.End,
		Page:      params.Page,
		PageSize:  params.PageSize,
	})
	if err != nil {
		return OperationLogPage{}, err
	}
	detail := canViewSensitiveCallLogs(principal)
	for index := range items {
		if !detail {
			items[index].ClientIP = maskClientIP(items[index].ClientIP)
		}
		// Operation views never expose opaque legacy details, user agents or error bodies.
		items[index].Detail = ""
		items[index].UserAgent = ""
		if items[index].Status {
			items[index].ErrorMessage = ""
		} else {
			items[index].ErrorMessage = fmt.Sprintf("HTTP %d", items[index].StatusCode)
		}
	}
	page, pageSize := pagination.Normalize(params.Page, params.PageSize, maxOperationLogPageSize)
	return OperationLogPage{
		Items:    items,
		Total:    total,
		Page:     page,
		PageSize: pageSize,
	}, nil
}

// OperationLogOverview 返回操作日志概览统计，供控制台卡片与运营告警共用。
func (s *Service) OperationLogOverview(ctx context.Context, principal rbac.Principal) (OperationLogStats, error) {
	if err := s.authz.Require(ctx, principal, operationLogPermission); err != nil {
		return OperationLogStats{}, err
	}
	return s.repo.OperationLogStats(ctx)
}

// RecordOperation 记录一条审计日志。
//
// 权限校验由调用方（各自业务 service）完成，本方法只负责写入与容错：
// 审计失败绝不阻断业务，错误仅用于日志。
const operationLogWriteTimeout = 2 * time.Second

func (s *Service) RecordOperation(ctx context.Context, rec OperationLogRecord) {
	// Preserve request values, but do not lose an already-completed operation
	// because the client disconnected. Keep the synchronous write bounded.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), operationLogWriteTimeout)
	defer cancel()
	id, err := s.repo.InsertOperationLog(ctx, rec)
	if err != nil {
		hlog.CtxWarnf(ctx, "operation log write failed: %s", masking.Error(err))
		return
	}
	// 地区解析是旁路：只在写入缺地区、且确实留有明文 IP 时才排入后台队列。
	// 调用方（如登录审计）自带地区时不重复查询上游。
	if strings.TrimSpace(rec.Country) == "" && strings.TrimSpace(rec.Province) == "" && strings.TrimSpace(rec.City) == "" {
		s.enqueueRegionBackfill(id, rec.ClientIP)
	}
}

// maskClientIP 把 IPv4 最后一节、IPv6 最后一组替换为 *，保留网段用于排障。
func maskClientIP(ip string) string {
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return ""
	}
	if strings.Contains(ip, ".") {
		parts := strings.Split(ip, ".")
		if len(parts) == 4 {
			parts[3] = "*"
			return strings.Join(parts, ".")
		}
	}
	if strings.Contains(ip, ":") {
		idx := strings.LastIndex(ip, ":")
		return ip[:idx+1] + "*"
	}
	return "***"
}

// maskOperationDetail 对审计明细做与调用日志一致的敏感串剔除。
func maskOperationDetail(detail string) string {
	if strings.TrimSpace(detail) == "" {
		return ""
	}
	return networkAddressPattern.ReplaceAllString(detail, "[redacted]")
}
