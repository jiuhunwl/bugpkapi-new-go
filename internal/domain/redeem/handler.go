package redeem

import (
	"bytes"
	"context"
	"strconv"
	"strings"

	apperrors "bugpk-api/internal/pkg/errors"
	"bugpk-api/internal/pkg/jsonrequest"
	"bugpk-api/internal/pkg/pagination"
	"bugpk-api/internal/pkg/response"

	hertzapp "github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"github.com/cloudwego/hertz/pkg/route"
)

type Handler struct{ service *Service }

func NewHandler(service *Service) *Handler { return &Handler{service: service} }

// RegisterSystem 注册管理端路由（/system-api/redeem，随 systemGroup 获得
// 操作审计中间件；敏感操作另由 service 显式写审计）。
func (h *Handler) RegisterSystem(group *route.RouterGroup) {
	group.GET("/types", h.listTypes)
	group.POST("/types", h.createType)
	group.PUT("/types/:id", h.updateType)
	group.PUT("/types/:id/status", h.updateTypeStatus)
	group.POST("/types/:id/codes", h.generateCodes)
	group.GET("/codes", h.listCodes)
	group.GET("/codes/export", h.exportCodes)
	group.POST("/codes/:id/void", h.voidCode)
	group.GET("/records", h.listRecords)
}

// RegisterUser 注册用户端路由（/system-api/user/redeem，登录态）。
func (h *Handler) RegisterUser(group *route.RouterGroup) {
	group.POST("/code", h.redeem)
	group.GET("/records", h.userRecords)
}

func (h *Handler) listTypes(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	result, err := h.service.ListTypes(ctx, p, codeListParams(c))
	respond(c, result, err)
}

func (h *Handler) createType(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	var req SaveTypeRequest
	if !bind(c, &req) {
		return
	}
	respond(c, true, h.service.SaveType(ctx, p, 0, req))
}

func (h *Handler) updateType(ctx context.Context, c *hertzapp.RequestContext) {
	p, id, ok := h.principalID(ctx, c)
	if !ok {
		return
	}
	var req SaveTypeRequest
	if !bind(c, &req) {
		return
	}
	respond(c, true, h.service.SaveType(ctx, p, id, req))
}

func (h *Handler) updateTypeStatus(ctx context.Context, c *hertzapp.RequestContext) {
	p, id, ok := h.principalID(ctx, c)
	if !ok {
		return
	}
	var req struct {
		Status int `json:"status"`
	}
	if !bind(c, &req) {
		return
	}
	respond(c, true, h.service.UpdateTypeStatus(ctx, p, id, req.Status))
}

func (h *Handler) generateCodes(ctx context.Context, c *hertzapp.RequestContext) {
	p, id, ok := h.principalID(ctx, c)
	if !ok {
		return
	}
	var req GenerateCodesRequest
	if !bind(c, &req) {
		return
	}
	result, err := h.service.GenerateCodes(ctx, p, id, req, h.AuditMeta(ctx, c))
	respond(c, result, err)
}

func (h *Handler) listCodes(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	result, err := h.service.ListCodes(ctx, p, codeListParams(c))
	respond(c, result, err)
}

func (h *Handler) exportCodes(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	typeID := queryInt64(c, "type_id", 0)
	if typeID <= 0 {
		response.FromError(c, apperrors.New(consts.StatusBadRequest, "缺少类型参数"))
		return
	}
	filename, data, err := h.service.ExportCodes(ctx, p, typeID, h.AuditMeta(ctx, c))
	if err != nil {
		response.FromError(c, err)
		return
	}
	c.Response.Header.SetContentType("application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	c.Response.Header.Set("Content-Disposition", "attachment; filename=\""+filename+"\"")
	c.Response.Header.Set("Cache-Control", "private, no-store")
	c.SetBodyStream(bytes.NewReader(data), len(data))
}

func (h *Handler) voidCode(ctx context.Context, c *hertzapp.RequestContext) {
	p, id, ok := h.principalID(ctx, c)
	if !ok {
		return
	}
	respond(c, true, h.service.VoidCode(ctx, p, id, h.AuditMeta(ctx, c)))
}

func (h *Handler) listRecords(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	result, err := h.service.ListRecords(ctx, p, codeListParams(c))
	respond(c, result, err)
}

func (h *Handler) redeem(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	var req RedeemRequest
	if !bind(c, &req) {
		return
	}
	result, err := h.service.Redeem(ctx, p, req.Code, clientIP(c), SourceConsole)
	respond(c, result, err)
}

func (h *Handler) userRecords(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	result, err := h.service.UserRecords(ctx, p, codeListParams(c))
	respond(c, result, err)
}

// ---------------------------------------------------------------- 助手（与 packagepkg handler 同形）

func (h *Handler) principal(ctx context.Context, c *hertzapp.RequestContext) (Principal, bool) {
	p, err := h.service.Authenticate(ctx, bearer(c))
	if err != nil {
		response.FromError(c, err)
		return Principal{}, false
	}
	return p, true
}

func (h *Handler) principalID(ctx context.Context, c *hertzapp.RequestContext) (Principal, int64, bool) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return Principal{}, 0, false
	}
	id, err := pathID(c)
	if err != nil {
		response.FromError(c, err)
		return Principal{}, 0, false
	}
	return p, id, true
}

// AuditMeta 从请求上下文提取审计用 HTTP 信息（领域不依赖 hertz 类型，
// service 侧以 AuditMeta 结构接收）。
func (h *Handler) AuditMeta(ctx context.Context, c *hertzapp.RequestContext) AuditMeta {
	_ = ctx
	return AuditMeta{
		Method:    string(c.Method()),
		Path:      c.FullPath(),
		ClientIP:  clientIP(c),
		UserAgent: string(c.UserAgent()),
	}
}

func respond(c *hertzapp.RequestContext, data any, err error) {
	if err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, data)
}

func bind(c *hertzapp.RequestContext, target any) bool {
	if err := jsonrequest.BindHertz(c, target, jsonrequest.DefaultMaxBodyBytes); err != nil {
		response.FromError(c, err)
		return false
	}
	return true
}

func bearer(c *hertzapp.RequestContext) string {
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

// clientIP 取可信来源 IP（router.go 已装配 TrustedProxyClientIP：
// 仅当 RemoteAddr∈受信代理才采信 XFF，否则用 RemoteAddr）。
func clientIP(c *hertzapp.RequestContext) string {
	return c.ClientIP()
}

func codeListParams(c *hertzapp.RequestContext) ListParams {
	params := ListParams{
		Keyword:  strings.TrimSpace(string(c.Query("keyword"))),
		TypeID:   queryInt64(c, "type_id", 0),
		Page:     queryInt(c, "page", 1),
		PageSize: queryInt(c, "pageSize", pagination.DefaultPageSize),
	}
	if raw := strings.TrimSpace(string(c.Query("status"))); raw != "" {
		if value, err := strconv.Atoi(raw); err == nil {
			params.Status = &value
		}
	}
	if raw := strings.TrimSpace(string(c.Query("source"))); raw != "" {
		if value, err := strconv.Atoi(raw); err == nil {
			params.Source = &value
		}
	}
	return params
}

func queryInt(c *hertzapp.RequestContext, key string, fallback int) int {
	raw := strings.TrimSpace(string(c.Query(key)))
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0
	}
	return value
}

func queryInt64(c *hertzapp.RequestContext, key string, fallback int64) int64 {
	raw := strings.TrimSpace(string(c.Query(key)))
	if raw == "" {
		return fallback
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return -1
	}
	return value
}
