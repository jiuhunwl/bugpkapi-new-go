package openapi

import (
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

// RegisterUser 注册管理面（控制台 JWT）自助路由：
//
//	/system-api/user/open-api-keys ...  本人 Key 的签发/重置/吊销/列表
//	/system-api/user/open-api/switch    自助开关（GET 读真实状态 / POST 改）
//
// admin 通过 userId 参数可跨账号签发（服务端校验管理员身份）。
func (h *Handler) RegisterUser(group *route.RouterGroup) {
	group.GET("/open-api-keys", h.listKeys)
	group.POST("/open-api-keys", h.issueKey)
	group.POST("/open-api-keys/options", h.scopeOptions)
	group.POST("/open-api-keys/:id/reset", h.resetKey)
	group.POST("/open-api-keys/:id/revoke", h.revokeKey)
	group.GET("/open-api/switch", h.getSelfSwitch)
	group.POST("/open-api/switch", h.selfSwitch)
}

// RegisterAdmin 注册管理员专属路由：
//
//	/system-api/system/open-api/config          全局开关与阈值
//	/system-api/system/users/:id/open-api-disabled  单账号强制禁用
//	/system-api/system/users/:id/open-api-keys/revoke-all  一键吊销全部 Key
//	/system-api/system/open-api-keys            跨账号列表
func (h *Handler) RegisterAdmin(group *route.RouterGroup) {
	group.GET("/open-api/config", h.getConfig)
	group.PUT("/open-api/config", h.updateConfig)
	group.PUT("/users/:id/open-api-disabled", h.setUserDisabled)
	group.POST("/users/:id/open-api-keys/revoke-all", h.revokeAllKeys)
	group.GET("/open-api-keys", h.listAllKeys)
}

// RegisterOpen 注册开放 API 平面（X-Open-Key 鉴权 + 端点级 scope 门）。
// prefix 由调用方给出（形如 /open-api）。
func (h *Handler) RegisterOpen(group *route.RouterGroup) {
	group.Use(h.service.OpenAuthMiddleware())

	group.GET("/account", RequireScope(EndpointAccountRead), h.openAccount)
	group.GET("/account/packages", RequireScope(EndpointPackageRead), h.openAccountPackages)
	group.POST("/api-keys", RequireScope(EndpointAPIKeyWrite), h.openCreateAPIKey)
	group.GET("/api-keys/:id", RequireScope(EndpointAPIKeyRead), h.openAPIKeyDetail)
	group.POST("/orders", RequireScope(EndpointOrderCreate), h.openCreateOrder)
	group.POST("/redeem", RequireScope(EndpointRedeemWrite), h.openRedeem)
	group.POST("/redeem-codes", RequireScope(EndpointRedeemCodeGenerate), h.openGenerateCodes)
}

// ---------------------------------------------------------------- 管理面

func (h *Handler) listKeys(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	items, total, err := h.service.ListKeys(ctx, p, listParamsOf(c))
	respondPage(c, items, total, err)
}

func (h *Handler) listAllKeys(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	params := listParamsOf(c)
	params.Scope = "all"
	items, total, err := h.service.ListKeys(ctx, p, params)
	respondPage(c, items, total, err)
}

func (h *Handler) scopeOptions(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	var req struct {
		UserID int64 `json:"userId"`
	}
	if !bind(c, &req) {
		return
	}
	options, err := h.service.ScopeOptions(ctx, p, req.UserID)
	respond(c, options, err)
}

func (h *Handler) issueKey(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	var req IssueKeyRequest
	if !bind(c, &req) {
		return
	}
	result, err := h.service.IssueKey(ctx, p, req)
	respond(c, result, err)
}

func (h *Handler) resetKey(ctx context.Context, c *hertzapp.RequestContext) {
	p, id, ok := h.principalID(ctx, c)
	if !ok {
		return
	}
	result, err := h.service.ResetKey(ctx, p, id)
	respond(c, result, err)
}

func (h *Handler) revokeKey(ctx context.Context, c *hertzapp.RequestContext) {
	p, id, ok := h.principalID(ctx, c)
	if !ok {
		return
	}
	respond(c, true, h.service.RevokeKey(ctx, p, id))
}

func (h *Handler) getSelfSwitch(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	status, err := h.service.SelfSwitchStatus(ctx, p)
	respond(c, status, err)
}

func (h *Handler) selfSwitch(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	var req struct {
		Enabled *bool `json:"enabled"`
	}
	if !bind(c, &req) {
		return
	}
	if req.Enabled == nil {
		response.FromError(c, apperrors.New(consts.StatusBadRequest, "缺少 enabled 参数"))
		return
	}
	respond(c, true, h.service.SetSelfSwitch(ctx, p, *req.Enabled))
}

func (h *Handler) getConfig(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	cfg, err := h.service.Config(ctx, p)
	respond(c, cfg, err)
}

func (h *Handler) updateConfig(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	var req ConfigRequest
	if !bind(c, &req) {
		return
	}
	respond(c, true, h.service.UpdateConfig(ctx, p, req))
}

func (h *Handler) setUserDisabled(ctx context.Context, c *hertzapp.RequestContext) {
	p, id, ok := h.principalID(ctx, c)
	if !ok {
		return
	}
	var req struct {
		Disabled *bool `json:"disabled"`
	}
	if !bind(c, &req) {
		return
	}
	if req.Disabled == nil {
		response.FromError(c, apperrors.New(consts.StatusBadRequest, "缺少 disabled 参数"))
		return
	}
	respond(c, true, h.service.SetUserDisabled(ctx, p, id, *req.Disabled))
}

func (h *Handler) revokeAllKeys(ctx context.Context, c *hertzapp.RequestContext) {
	p, id, ok := h.principalID(ctx, c)
	if !ok {
		return
	}
	count, err := h.service.RevokeAllUserKeys(ctx, p, id)
	respond(c, map[string]any{"revoked": count}, err)
}

// ---------------------------------------------------------------- 开放面

func (h *Handler) openAccount(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := openPrincipal(c)
	if !ok {
		return
	}
	info, err := h.service.AccountInfo(ctx, p)
	respond(c, info, err)
}

func (h *Handler) openAccountPackages(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := openPrincipal(c)
	if !ok {
		return
	}
	page := queryInt(c, "page", 1)
	pageSize := queryInt(c, "page_size", pagination.DefaultPageSize)
	result, err := h.service.AccountPackages(ctx, p, page, pageSize)
	respond(c, result, err)
}

func (h *Handler) openCreateAPIKey(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := openPrincipal(c)
	if !ok {
		return
	}
	var req CreateAPIKeyRequest
	if !bind(c, &req) {
		return
	}
	idempotencyKey := strings.TrimSpace(string(c.Request.Header.Peek("Idempotency-Key")))
	result, err := h.service.CreateAPIKey(ctx, p, req, idempotencyKey)
	respond(c, result, err)
}

func (h *Handler) openAPIKeyDetail(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := openPrincipal(c)
	if !ok {
		return
	}
	id, err := pathID(c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	item, err := h.service.APIKeyDetail(ctx, p, id)
	respond(c, item, err)
}

func (h *Handler) openCreateOrder(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := openPrincipal(c)
	if !ok {
		return
	}
	var req CreateOrderRequest
	if !bind(c, &req) {
		return
	}
	order, err := h.service.CreateOrder(ctx, p, req)
	respond(c, order, err)
}

func (h *Handler) openRedeem(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := openPrincipal(c)
	if !ok {
		return
	}
	var req RedeemCdkRequest
	if !bind(c, &req) {
		return
	}
	result, err := h.service.RedeemCDK(ctx, p, req.Code)
	respond(c, result, err)
}

func (h *Handler) openGenerateCodes(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := openPrincipal(c)
	if !ok {
		return
	}
	var req GenerateCodesRequest
	if !bind(c, &req) {
		return
	}
	result, err := h.service.GenerateCDKs(ctx, p, req)
	respond(c, result, err)
}

// ---------------------------------------------------------------- 助手

func openPrincipal(c *hertzapp.RequestContext) (OpenPrincipal, bool) {
	principal, ok := OpenPrincipalFrom(c)
	if !ok {
		response.FromError(c, openError(CodeKeyMissing, consts.StatusUnauthorized, "缺少开放 Key 身份"))
		return OpenPrincipal{}, false
	}
	return principal, true
}

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

func respond(c *hertzapp.RequestContext, data any, err error) {
	if err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, data)
}

func respondPage(c *hertzapp.RequestContext, items any, total int64, err error) {
	if err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, map[string]any{"items": items, "total": total})
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

func listParamsOf(c *hertzapp.RequestContext) listParams {
	return listParams{
		Keyword:  strings.TrimSpace(string(c.Query("keyword"))),
		Page:     queryInt(c, "page", 1),
		PageSize: queryInt(c, "pageSize", pagination.DefaultPageSize),
		Scope:    strings.TrimSpace(string(c.Query("scope"))),
	}
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
