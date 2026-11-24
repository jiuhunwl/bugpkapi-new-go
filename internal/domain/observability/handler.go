package observability

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"bugpk-api/internal/domain/rbac"
	"bugpk-api/internal/pkg/businessclock"
	apperrors "bugpk-api/internal/pkg/errors"
	"bugpk-api/internal/pkg/jsonrequest"
	"bugpk-api/internal/pkg/masking"
	"bugpk-api/internal/pkg/response"

	hertzapp "github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/common/hlog"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"github.com/cloudwego/hertz/pkg/protocol/http1/resp"
	"github.com/cloudwego/hertz/pkg/route"
)

type Handler struct {
	service *Service
}

func NewHandler(service *Service) *Handler {
	return &Handler{service: service}
}

func (h *Handler) RegisterInterface(group *route.RouterGroup) {
	group.GET("/call-log/export", h.export)
	group.GET("/call-log/list", h.adminList)
	group.GET("/call-log/:id", h.adminDetail)
	group.DELETE("/call-log/cleanup", h.cleanup)
	group.GET("/call-log/suggest/users", h.suggestUsers)
	group.GET("/call-log/suggest/interfaces", h.suggestInterfaces)
	group.GET("/call-log/suggest/api-keys", h.suggestAPIKeys)
}

func (h *Handler) adminList(ctx context.Context, c *hertzapp.RequestContext) {
	p, err := h.principal(ctx, c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	params, err := callLogParams(c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	result, err := h.service.AdminCallLogs(ctx, p, params)
	respond(c, result, err)
}

func (h *Handler) export(ctx context.Context, c *hertzapp.RequestContext) {
	p, err := h.principal(ctx, c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	params, err := callLogParams(c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	writer := &streamingResponseWriter{c: c}
	if err := h.service.StreamExportCallLogs(ctx, p, params, writer); err != nil {
		if !writer.started {
			response.FromError(c, err)
			return
		}
		// Headers/chunks already left the server. Appending JSON would corrupt
		// the CSV and falsely complete the download; terminate the stream instead.
		hlog.CtxErrorf(ctx, "call log export interrupted: %s", masking.Error(err))
		c.SetConnectionClose()
		if conn := c.GetConn(); conn != nil {
			_ = conn.Close()
		}
	}
}

func exportCallLogFilename(now time.Time) string {
	return fmt.Sprintf("bugpk-api-call-logs-%s.csv", now.Format("20060102-150405"))
}

// streamingResponseWriter 将 CSV 分批写入适配为 Hertz 分块响应。
type streamingResponseWriter struct {
	c       *hertzapp.RequestContext
	started bool
}

func (w *streamingResponseWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	writer := w.c.Response.GetHijackWriter()
	if writer == nil {
		w.c.Response.Header.Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", exportCallLogFilename(time.Now())))
		w.c.Response.Header.Set("Content-Type", "text/csv; charset=utf-8")
		w.c.Response.Header.Set("Cache-Control", "no-store")
		w.c.Response.SetStatusCode(consts.StatusOK)
		writer = resp.NewChunkedBodyWriter(&w.c.Response, w.c.GetWriter())
		w.c.Response.HijackWriter(writer)
	}
	w.started = true
	n, err := writer.Write(p)
	if err != nil {
		return n, err
	}
	if err := writer.Flush(); err != nil {
		return n, err
	}
	return n, nil
}

func (h *Handler) RegisterUser(group *route.RouterGroup) {
	group.GET("/call-logs", h.userList)
	group.GET("/call-logs/:id", h.userDetail)
	group.DELETE("/call-logs/:id", h.hideUserLog)
	group.GET("/call-log-cleanup-config", h.cleanupConfig)
	group.PUT("/call-log-cleanup-config", h.saveCleanupConfig)
}

func (h *Handler) RegisterDashboard(group *route.RouterGroup) {
	group.GET("/overview", h.overview)
	group.GET("/call-trend", h.callTrend)
	group.GET("/region-ranking", h.regionRanking)
	group.GET("/hot-apis", h.hotAPIs)
}

// RegisterOperationLog 挂载系统操作日志（审计）路由。
// 路径以 /operation-log 为前缀，与调用日志 /call-log 明确区分。
func (h *Handler) RegisterOperationLog(group *route.RouterGroup) {
	group.GET("/operation-log/list", h.operationLogList)
	group.GET("/operation-log/mine", h.myOperationLogList)
	group.GET("/operation-log/overview", h.operationLogOverview)
	group.POST("/operation-log/:id/region", h.resolveOperationLogRegion)
}

// resolveOperationLogRegion 按需解析单条审计记录的 IP 归属地并回写。
// POST 语义：该操作会写库（回填地区列），因此不放在 GET 上。
func (h *Handler) resolveOperationLogRegion(ctx context.Context, c *hertzapp.RequestContext) {
	p, id, ok := h.principalID(ctx, c)
	if !ok {
		return
	}
	result, err := h.service.ResolveOperationLogRegion(ctx, p, id)
	respond(c, result, err)
}

func (h *Handler) operationLogList(ctx context.Context, c *hertzapp.RequestContext) {
	p, err := h.principal(ctx, c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	params, err := operationLogParams(c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	result, err := h.service.AdminOperationLogs(ctx, p, params)
	respond(c, result, err)
}

func (h *Handler) myOperationLogList(ctx context.Context, c *hertzapp.RequestContext) {
	p, err := h.principal(ctx, c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	params, err := operationLogParams(c, true)
	if err != nil {
		response.FromError(c, err)
		return
	}
	result, err := h.service.MyOperationLogs(ctx, p, params)
	respond(c, result, err)
}

func (h *Handler) operationLogOverview(ctx context.Context, c *hertzapp.RequestContext) {
	p, err := h.principal(ctx, c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	result, err := h.service.OperationLogOverview(ctx, p)
	respond(c, result, err)
}

func (h *Handler) adminDetail(ctx context.Context, c *hertzapp.RequestContext) {
	p, id, ok := h.principalID(ctx, c)
	if !ok {
		return
	}
	includeSensitive, err := optionalBool(c, "unmasked")
	if err != nil {
		response.FromError(c, err)
		return
	}
	result, err := h.service.AdminCallLogDetail(ctx, p, id, includeSensitive)
	respond(c, result, err)
}

func (h *Handler) cleanup(ctx context.Context, c *hertzapp.RequestContext) {
	p, err := h.principal(ctx, c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	result, err := h.service.CleanupLogs(ctx, p, string(c.Query("before")))
	respond(c, result, err)
}

// suggestUsers serves the remote-search protocol for the user filter: a
// bounded keyword search that returns a small option list (not the full
// user table). Frontend debounce/cancel is handled by the caller.
func (h *Handler) suggestUsers(ctx context.Context, c *hertzapp.RequestContext) {
	p, err := h.principal(ctx, c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	keyword, limit, err := suggestParams(c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	result, err := h.service.SuggestUsers(ctx, p, keyword, limit)
	respond(c, result, err)
}

func (h *Handler) suggestInterfaces(ctx context.Context, c *hertzapp.RequestContext) {
	p, err := h.principal(ctx, c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	keyword, limit, err := suggestParams(c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	result, err := h.service.SuggestInterfaces(ctx, p, keyword, limit)
	respond(c, result, err)
}

func (h *Handler) suggestAPIKeys(ctx context.Context, c *hertzapp.RequestContext) {
	p, err := h.principal(ctx, c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	keyword, limit, err := suggestParams(c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	result, err := h.service.SuggestAPIKeys(ctx, p, keyword, limit)
	respond(c, result, err)
}

// suggestParams extracts the shared remote-search query params. An omitted
// keyword means "return top candidates"; limit is clamped by the service.
func suggestParams(c *hertzapp.RequestContext) (string, int, error) {
	keyword := strings.TrimSpace(string(c.Query("keyword")))
	limit, err := optionalNonNegativeInt64(c, "limit")
	if err != nil {
		return "", 0, err
	}
	if limit > maxSuggestLimit {
		return "", 0, apperrors.New(consts.StatusBadRequest, "limit must not exceed 20")
	}
	return keyword, int(limit), nil
}

func (h *Handler) userList(ctx context.Context, c *hertzapp.RequestContext) {
	p, err := h.principal(ctx, c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	params, err := callLogParams(c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	result, err := h.service.UserCallLogs(ctx, p, params)
	respond(c, result, err)
}

func (h *Handler) userDetail(ctx context.Context, c *hertzapp.RequestContext) {
	p, id, ok := h.principalID(ctx, c)
	if !ok {
		return
	}
	result, err := h.service.UserCallLogDetail(ctx, p, id)
	respond(c, result, err)
}

func (h *Handler) hideUserLog(ctx context.Context, c *hertzapp.RequestContext) {
	p, id, ok := h.principalID(ctx, c)
	if !ok {
		return
	}
	respond(c, true, h.service.HideUserLog(ctx, p, id))
}

func (h *Handler) cleanupConfig(ctx context.Context, c *hertzapp.RequestContext) {
	p, err := h.principal(ctx, c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	result, err := h.service.CleanupConfig(ctx, p)
	respond(c, result, err)
}

func (h *Handler) saveCleanupConfig(ctx context.Context, c *hertzapp.RequestContext) {
	p, err := h.principal(ctx, c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	var req CleanupConfigRequest
	if err := bindJSON(c, &req); err != nil {
		response.FromError(c, err)
		return
	}
	respond(c, true, h.service.SaveCleanupConfig(ctx, p, req))
}

func (h *Handler) overview(ctx context.Context, c *hertzapp.RequestContext) {
	p, err := h.principal(ctx, c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	result, err := h.service.Overview(ctx, p)
	respond(c, result, err)
}

func (h *Handler) callTrend(ctx context.Context, c *hertzapp.RequestContext) {
	p, err := h.principal(ctx, c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	result, err := h.service.CallTrend(ctx, p, queryInt(c, "days", 7))
	respond(c, result, err)
}

func (h *Handler) regionRanking(ctx context.Context, c *hertzapp.RequestContext) {
	p, err := h.principal(ctx, c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	result, err := h.service.RegionRanking(ctx, p, queryInt(c, "limit", 10))
	respond(c, result, err)
}

func (h *Handler) hotAPIs(ctx context.Context, c *hertzapp.RequestContext) {
	p, err := h.principal(ctx, c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	result, err := h.service.HotAPIs(ctx, p, queryInt(c, "limit", 10))
	respond(c, result, err)
}

func (h *Handler) principal(ctx context.Context, c *hertzapp.RequestContext) (rbac.Principal, error) {
	return h.service.Authenticate(ctx, bearerToken(c))
}

func (h *Handler) principalID(ctx context.Context, c *hertzapp.RequestContext) (rbac.Principal, int64, bool) {
	principal, err := h.principal(ctx, c)
	if err != nil {
		response.FromError(c, err)
		return rbac.Principal{}, 0, false
	}
	id, err := pathID(c)
	if err != nil {
		response.FromError(c, err)
		return rbac.Principal{}, 0, false
	}
	return principal, id, true
}

func callLogParams(c *hertzapp.RequestContext) (CallLogListParams, error) {
	params := CallLogListParams{
		APICode:       strings.TrimSpace(string(c.Query("apiCode"))),
		BillingStatus: strings.ToUpper(strings.TrimSpace(string(c.Query("billingStatus")))),
		ChargeType:    strings.ToUpper(strings.TrimSpace(string(c.Query("chargeType")))),
		ClientIP:      strings.TrimSpace(string(c.Query("clientIp"))),
		Cursor:        strings.TrimSpace(string(c.Query("cursor"))),
		Keyword:       strings.TrimSpace(string(c.Query("keyword"))),
		Region:        strings.TrimSpace(string(c.Query("region"))),
		TraceID:       strings.TrimSpace(string(c.Query("traceId"))),
	}
	var err error
	if raw := strings.TrimSpace(string(c.Query("includeTotal"))); raw != "" {
		include, parseErr := optionalBool(c, "includeTotal")
		if parseErr != nil {
			return CallLogListParams{}, parseErr
		}
		params.SkipTotal = !include
	}
	if params.IncludeSensitive, err = optionalBool(c, "unmasked"); err != nil {
		return CallLogListParams{}, err
	}
	if params.APIKeyID, err = optionalPositiveInt64(c, "apiKeyId"); err != nil {
		return CallLogListParams{}, err
	}
	if params.InterfaceID, err = optionalPositiveInt64(c, "interfaceId"); err != nil {
		return CallLogListParams{}, err
	}
	if params.UserID, err = optionalPositiveInt64(c, "userId"); err != nil {
		return CallLogListParams{}, err
	}
	if params.MinElapsedMs, err = optionalNonNegativeInt64(c, "minElapsedMs"); err != nil {
		return CallLogListParams{}, err
	}
	if params.MaxElapsedMs, err = optionalNonNegativeInt64(c, "maxElapsedMs"); err != nil {
		return CallLogListParams{}, err
	}
	if params.Success, err = optionalBinary(c, "success"); err != nil {
		return CallLogListParams{}, err
	}
	if params.Billable, err = optionalBinary(c, "billable"); err != nil {
		return CallLogListParams{}, err
	}
	if params.BillingOnly, err = optionalBool(c, "billingOnly"); err != nil {
		return CallLogListParams{}, err
	}
	if params.Page, err = boundedInt(c, "page", 1, 1, maxCallLogPage); err != nil {
		return CallLogListParams{}, err
	}
	if params.PageSize, err = boundedInt(c, "pageSize", 20, 1, maxCallLogPageSize); err != nil {
		return CallLogListParams{}, err
	}
	if params.StartTime, err = optionalLogTime(c, "startTime"); err != nil {
		return CallLogListParams{}, err
	}
	if params.EndTime, err = optionalLogTime(c, "endTime"); err != nil {
		return CallLogListParams{}, err
	}
	if err := validateCallLogParams(params); err != nil {
		return CallLogListParams{}, err
	}
	if err := normalizeCallLogFilterWindow(&params, time.Now()); err != nil {
		return CallLogListParams{}, err
	}
	return params, nil
}

// operationLogParams 解析操作日志列表的查询参数。
//
// 时间参数复用 optionalLogTime（同一套格式校验），页深上限与仓储保持一致，
// 避免 HTTP 层放行一个仓储必然夹取的页码造成「请求了但结果不符」的错觉。
func operationLogParams(c *hertzapp.RequestContext, ownActor ...bool) (OperationLogListParams, error) {
	params := OperationLogListParams{
		Action:   strings.TrimSpace(string(c.Query("action"))),
		ClientIP: strings.TrimSpace(string(c.Query("clientIp"))),
		Keyword:  strings.TrimSpace(string(c.Query("keyword"))),
		Module:   strings.TrimSpace(string(c.Query("module"))),
	}
	var err error
	if len(ownActor) == 0 || !ownActor[0] {
		params.Username = strings.TrimSpace(string(c.Query("username")))
		if params.UserID, err = optionalPositiveInt64(c, "userId"); err != nil {
			return OperationLogListParams{}, err
		}
	}
	if raw := strings.TrimSpace(string(c.Query("status"))); raw != "" {
		ok, parseErr := optionalBool(c, "status")
		if parseErr != nil {
			return OperationLogListParams{}, parseErr
		}
		params.Status = &ok
	}
	if params.Page, err = boundedInt(c, "page", 1, 1, maxOperationLogPage); err != nil {
		return OperationLogListParams{}, err
	}
	if params.PageSize, err = boundedInt(c, "pageSize", 20, 1, maxOperationLogPageSize); err != nil {
		return OperationLogListParams{}, err
	}
	if params.Start, err = optionalLogTime(c, "startTime"); err != nil {
		return OperationLogListParams{}, err
	}
	if params.End, err = optionalLogTime(c, "endTime"); err != nil {
		return OperationLogListParams{}, err
	}
	if params.Start != "" && params.End != "" && params.Start > params.End {
		return OperationLogListParams{}, apperrors.New(consts.StatusBadRequest, "startTime must not be later than endTime")
	}
	return params, nil
}

func optionalPositiveInt64(c *hertzapp.RequestContext, key string) (int64, error) {
	raw := strings.TrimSpace(string(c.Query(key)))
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value <= 0 {
		return 0, apperrors.New(consts.StatusBadRequest, key+" must be a positive integer")
	}
	return value, nil
}

func optionalNonNegativeInt64(c *hertzapp.RequestContext, key string) (int64, error) {
	raw := strings.TrimSpace(string(c.Query(key)))
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < 0 {
		return 0, apperrors.New(consts.StatusBadRequest, key+" must be a non-negative integer")
	}
	return value, nil
}

func optionalBinary(c *hertzapp.RequestContext, key string) (*int, error) {
	raw := strings.TrimSpace(string(c.Query(key)))
	if raw == "" {
		return nil, nil
	}
	if raw != "0" && raw != "1" {
		return nil, apperrors.New(consts.StatusBadRequest, key+" must be 0 or 1")
	}
	value, _ := strconv.Atoi(raw)
	return &value, nil
}

func optionalBool(c *hertzapp.RequestContext, key string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(string(c.Query(key)))) {
	case "", "0", "false", "no":
		return false, nil
	case "1", "true", "yes":
		return true, nil
	default:
		return false, apperrors.New(consts.StatusBadRequest, key+" must be a boolean")
	}
}

func boundedInt(c *hertzapp.RequestContext, key string, fallback, min, max int) (int, error) {
	raw := strings.TrimSpace(string(c.Query(key)))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < min || value > max {
		return 0, apperrors.New(consts.StatusBadRequest, fmt.Sprintf("%s must be between %d and %d", key, min, max))
	}
	return value, nil
}

func optionalLogTime(c *hertzapp.RequestContext, key string) (string, error) {
	raw := strings.TrimSpace(string(c.Query(key)))
	if raw == "" {
		return "", nil
	}
	for _, layout := range []string{"2006-01-02 15:04:05", time.RFC3339} {
		if parsed, err := time.ParseInLocation(layout, raw, businessclock.Location()); err == nil {
			return parsed.In(businessclock.Location()).Format("2006-01-02 15:04:05"), nil
		}
	}
	return "", apperrors.New(consts.StatusBadRequest, key+" must use YYYY-MM-DD HH:mm:ss or RFC3339")
}

// fuzzyInputTooShort 拒绝单个 ASCII 字符的模糊输入（至少 2 字符），
// 单个 CJK 字符有实际语义，放行。
func fuzzyInputTooShort(value string) bool {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return false
	}
	runes := []rune(trimmed)
	if len(runes) >= 2 {
		return false
	}
	return runes[0] < 0x4E00 || runes[0] > 0x9FFF
}

func validateCallLogParams(params CallLogListParams) error {
	if params.SkipTotal {
		start, startErr := time.ParseInLocation("2006-01-02 15:04:05", params.StartTime, businessclock.Location())
		end, endErr := time.ParseInLocation("2006-01-02 15:04:05", params.EndTime, businessclock.Location())
		if startErr != nil || endErr != nil || end.Before(start) || end.Sub(start) > maxCallLogFuzzyWindow {
			return apperrors.New(consts.StatusBadRequest, "includeTotal=false requires explicit startTime/endTime spanning at most 31 days")
		}
		if params.Page > 1 && params.Cursor == "" {
			return apperrors.New(consts.StatusBadRequest, "includeTotal=false requires cursor pagination after the first page")
		}
	}

	if fuzzyInputTooShort(params.Keyword) || fuzzyInputTooShort(params.Region) {
		return apperrors.New(consts.StatusBadRequest, "keyword and region filters need at least 2 characters (a single CJK character is allowed)")
	}
	if len(params.Keyword) > 256 || len(params.Region) > 128 ||
		len(params.TraceID) > 64 || len(params.ClientIP) > 64 || len(params.APICode) > 64 || len(params.Cursor) > 128 {
		return apperrors.New(consts.StatusBadRequest, "call-log filter text is too long")
	}
	if params.MaxElapsedMs > 0 && params.MinElapsedMs > params.MaxElapsedMs {
		return apperrors.New(consts.StatusBadRequest, "minElapsedMs must not exceed maxElapsedMs")
	}
	if params.StartTime != "" && params.EndTime != "" && params.StartTime > params.EndTime {
		return apperrors.New(consts.StatusBadRequest, "startTime must not be later than endTime")
	}
	if params.ChargeType != "" && !allowedCallLogChargeTypes[params.ChargeType] {
		return apperrors.New(consts.StatusBadRequest, "chargeType is invalid")
	}
	if params.BillingStatus != "" && !allowedCallLogBillingStatuses[params.BillingStatus] {
		return apperrors.New(consts.StatusBadRequest, "billingStatus is invalid")
	}
	return nil
}

const (
	maxCallLogFuzzyWindow      = 31 * 24 * time.Hour
	defaultCallLogFilterWindow = 30 * 24 * time.Hour
)

func normalizeCallLogFilterWindow(params *CallLogListParams, now time.Time) error {
	if params == nil || !callLogFilterNeedsWindow(*params) {
		return nil
	}
	parse := func(value string) (time.Time, error) {
		return time.ParseInLocation("2006-01-02 15:04:05", value, businessclock.Location())
	}
	var start, end time.Time
	var err error
	if params.StartTime != "" {
		start, err = parse(params.StartTime)
		if err != nil {
			return apperrors.New(consts.StatusBadRequest, "startTime is invalid")
		}
	}
	if params.EndTime != "" {
		end, err = parse(params.EndTime)
		if err != nil {
			return apperrors.New(consts.StatusBadRequest, "endTime is invalid")
		}
	}

	switch {
	case start.IsZero() && end.IsZero():
		end = now.In(businessclock.Location())
		start = end.Add(-defaultCallLogFilterWindow)
	case start.IsZero():
		start = end.Add(-defaultCallLogFilterWindow)
	case end.IsZero():
		end = start.Add(30 * 24 * time.Hour)
		if localNow := now.In(businessclock.Location()); end.After(localNow) {
			end = localNow
		}
	}
	if end.Before(start) || end.Sub(start) > maxCallLogFuzzyWindow {
		return apperrors.New(consts.StatusBadRequest, "keyword and region filters must use a time range no longer than 31 days")
	}
	params.StartTime = start.Format("2006-01-02 15:04:05")
	params.EndTime = end.Format("2006-01-02 15:04:05")
	return nil
}

func callLogFilterNeedsWindow(params CallLogListParams) bool {
	return strings.TrimSpace(params.Keyword) != "" ||
		strings.TrimSpace(params.Region) != "" ||
		params.MinElapsedMs > 0 ||
		params.MaxElapsedMs > 0 ||
		params.Success != nil ||
		params.Billable != nil ||
		strings.TrimSpace(params.ChargeType) != "" ||
		strings.TrimSpace(params.BillingStatus) != ""
}

var allowedCallLogChargeTypes = map[string]bool{
	"ADMIN": true, "BALANCE": true, "FREE": true, "GLOBAL_PACKAGE": true,
	"INTERFACE_PACKAGE": true, "MEMBER": true, "NO_CHARGE": true,
	"OPEN_PENDING": true, "POINT": true, "POINTS": true,
}

var allowedCallLogBillingStatuses = map[string]bool{
	"CONFIRMED": true, "FAILED": true, "NONE": true, "NO_CHARGE": true,
	"OPEN_PENDING": true, "RELEASED": true, "RESERVED": true,
}

func respond(c *hertzapp.RequestContext, data any, err error) {
	if err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, data)
}

func bindJSON(c *hertzapp.RequestContext, target any) error {
	return jsonrequest.BindHertz(c, target, jsonrequest.DefaultMaxBodyBytes)
}

func bearerToken(c *hertzapp.RequestContext) string {
	value := string(c.Request.Header.Peek("Authorization"))
	if strings.HasPrefix(strings.ToLower(value), "bearer ") {
		return strings.TrimSpace(value[7:])
	}
	return ""
}

func pathID(c *hertzapp.RequestContext) (int64, error) {
	id, err := strconv.ParseInt(strings.TrimSpace(c.Param("id")), 10, 64)
	if err != nil || id <= 0 {
		return 0, apperrors.New(consts.StatusBadRequest, "ID不合法")
	}
	return id, nil
}

func queryInt(c *hertzapp.RequestContext, key string, fallback int) int {
	raw := strings.TrimSpace(string(c.Query(key)))
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return value
}
