package packagepkg

import (
	"context"
	"strconv"
	"strings"

	apperrors "bugpk-api/internal/pkg/errors"
	"bugpk-api/internal/pkg/jsonrequest"
	"bugpk-api/internal/pkg/response"

	hertzapp "github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"github.com/cloudwego/hertz/pkg/route"
)

type Handler struct{ service *Service }

func NewHandler(service *Service) *Handler { return &Handler{service: service} }

func (h *Handler) RegisterPackage(group *route.RouterGroup) {
	group.GET("/global/list", h.listGlobal)
	group.POST("/global", h.createGlobal)
	group.PUT("/global/:id", h.updateGlobal)
	group.DELETE("/global/:id", h.deleteGlobal)
	group.GET("/interface/list", h.listInterface)
	group.POST("/interface", h.createInterface)
	group.PUT("/interface/:id", h.updateInterface)
	group.DELETE("/interface/:id", h.deleteInterface)
	group.GET("/point/list", h.listPoint)
	group.POST("/point", h.createPoint)
	group.PUT("/point/:id", h.updatePoint)
	group.DELETE("/point/:id", h.deletePoint)
	group.GET("/user-entitlement/list", h.listUserEntitlements)
	group.POST("/user-entitlement/grant", h.grantUserEntitlement)
	group.PUT("/user-entitlement/:id", h.updateUserEntitlement)
	group.DELETE("/user-entitlement/:id", h.deleteUserEntitlement)
	group.GET("/mall/global/list", h.mallGlobal)
	group.GET("/mall/interface/list", h.mallInterface)
	group.GET("/mall/interface/:id", h.mallInterfaceDetail)
	group.GET("/mall/point/list", h.mallPoint)
	group.POST("/mall/global/:id/purchase", h.purchaseGlobal)
	group.POST("/mall/interface/:id/purchase", h.purchaseInterface)
	group.POST("/mall/point/:id/purchase", h.purchasePoint)
}

func (h *Handler) RegisterUser(group *route.RouterGroup) {
	group.GET("/packages", h.userPackages)
	group.PUT("/packages/primary", h.setPrimaryPackage)
	group.GET("/wallet/transactions", h.walletTransactions)
	group.GET("/billing-records", h.userBillingRecords)
}

// setPrimaryPackage 由用户指定主套餐（id=0 取消）。只影响网关扣减顺序。
func (h *Handler) setPrimaryPackage(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	var req SetPrimaryPackageRequest
	if !bind(c, &req) {
		return
	}
	respond(c, true, h.service.SetPrimaryPackage(ctx, p, req))
}

func (h *Handler) listGlobal(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	result, err := h.service.ListGlobal(ctx, p, listParams(c))
	respond(c, result, err)
}
func (h *Handler) createGlobal(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	var req SaveGlobalPackageRequest
	if !bind(c, &req) {
		return
	}
	respond(c, true, h.service.SaveGlobal(ctx, p, 0, req))
}
func (h *Handler) updateGlobal(ctx context.Context, c *hertzapp.RequestContext) {
	p, id, ok := h.principalID(ctx, c)
	if !ok {
		return
	}
	var req SaveGlobalPackageRequest
	if !bind(c, &req) {
		return
	}
	respond(c, true, h.service.SaveGlobal(ctx, p, id, req))
}
func (h *Handler) deleteGlobal(ctx context.Context, c *hertzapp.RequestContext) {
	p, id, ok := h.principalID(ctx, c)
	if !ok {
		return
	}
	respond(c, true, h.service.DeleteGlobal(ctx, p, id, forceFlag(c)))
}
func (h *Handler) listInterface(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	result, err := h.service.ListInterface(ctx, p, listParams(c))
	respond(c, result, err)
}
func (h *Handler) createInterface(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	var req SaveInterfacePackageRequest
	if !bind(c, &req) {
		return
	}
	respond(c, true, h.service.SaveInterface(ctx, p, 0, req))
}
func (h *Handler) updateInterface(ctx context.Context, c *hertzapp.RequestContext) {
	p, id, ok := h.principalID(ctx, c)
	if !ok {
		return
	}
	var req SaveInterfacePackageRequest
	if !bind(c, &req) {
		return
	}
	respond(c, true, h.service.SaveInterface(ctx, p, id, req))
}
func (h *Handler) deleteInterface(ctx context.Context, c *hertzapp.RequestContext) {
	p, id, ok := h.principalID(ctx, c)
	if !ok {
		return
	}
	respond(c, true, h.service.DeleteInterface(ctx, p, id, forceFlag(c)))
}
func (h *Handler) listPoint(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	result, err := h.service.ListPoint(ctx, p, listParams(c))
	respond(c, result, err)
}
func (h *Handler) createPoint(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	var req SavePointPackageRequest
	if !bind(c, &req) {
		return
	}
	respond(c, true, h.service.SavePoint(ctx, p, 0, req))
}
func (h *Handler) updatePoint(ctx context.Context, c *hertzapp.RequestContext) {
	p, id, ok := h.principalID(ctx, c)
	if !ok {
		return
	}
	var req SavePointPackageRequest
	if !bind(c, &req) {
		return
	}
	respond(c, true, h.service.SavePoint(ctx, p, id, req))
}
func (h *Handler) deletePoint(ctx context.Context, c *hertzapp.RequestContext) {
	p, id, ok := h.principalID(ctx, c)
	if !ok {
		return
	}
	respond(c, true, h.service.DeletePoint(ctx, p, id, forceFlag(c)))
}
func (h *Handler) mallGlobal(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	result, err := h.service.MallGlobal(ctx, p, listParams(c))
	respond(c, result, err)
}
func (h *Handler) mallInterface(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	result, err := h.service.MallInterface(ctx, p, listParams(c))
	respond(c, result, err)
}
func (h *Handler) mallInterfaceDetail(ctx context.Context, c *hertzapp.RequestContext) {
	p, id, ok := h.principalID(ctx, c)
	if !ok {
		return
	}
	result, err := h.service.MallInterfaceDetail(ctx, p, id)
	respond(c, result, err)
}
func (h *Handler) mallPoint(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	result, err := h.service.MallPoint(ctx, p, listParams(c))
	respond(c, result, err)
}
func (h *Handler) purchaseGlobal(ctx context.Context, c *hertzapp.RequestContext) {
	p, id, ok := h.principalID(ctx, c)
	if !ok {
		return
	}
	respond(c, true, h.service.PurchaseGlobal(ctx, p, id, purchaseIdempotencyKey(c)))
}
func (h *Handler) purchaseInterface(ctx context.Context, c *hertzapp.RequestContext) {
	p, id, ok := h.principalID(ctx, c)
	if !ok {
		return
	}
	respond(c, true, h.service.PurchaseInterface(ctx, p, id, purchaseIdempotencyKey(c)))
}
func (h *Handler) purchasePoint(ctx context.Context, c *hertzapp.RequestContext) {
	p, id, ok := h.principalID(ctx, c)
	if !ok {
		return
	}
	respond(c, true, h.service.PurchasePoint(ctx, p, id, purchaseIdempotencyKey(c)))
}
func (h *Handler) listUserEntitlements(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	params := ListUserEntitlementParams{
		Keyword:  string(c.Query("keyword")),
		Page:     queryInt(c, "page", 1),
		PageSize: queryInt(c, "pageSize", 20),
		Scope:    string(c.Query("scope")),
	}
	if raw := strings.TrimSpace(string(c.Query("status"))); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil {
			value = 2
		}
		params.Status = &value
	}
	result, err := h.service.ListUserEntitlements(ctx, p, params)
	respond(c, result, err)
}

// grantUserEntitlement 管理端直接向用户下发套餐权益（隐藏套餐的唯一发放通道）。
func (h *Handler) grantUserEntitlement(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	var req GrantUserEntitlementRequest
	if !bind(c, &req) {
		return
	}
	result, err := h.service.GrantUserEntitlement(ctx, p, req)
	respond(c, result, err)
}
func (h *Handler) updateUserEntitlement(ctx context.Context, c *hertzapp.RequestContext) {
	p, id, ok := h.principalID(ctx, c)
	if !ok {
		return
	}
	var req UpdateUserEntitlementRequest
	if !bind(c, &req) {
		return
	}
	respond(c, true, h.service.UpdateUserEntitlement(ctx, p, id, req))
}
func (h *Handler) deleteUserEntitlement(ctx context.Context, c *hertzapp.RequestContext) {
	p, id, ok := h.principalID(ctx, c)
	if !ok {
		return
	}
	respond(c, true, h.service.DeleteUserEntitlement(ctx, p, id, string(c.Query("scope"))))
}
func (h *Handler) userPackages(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	result, err := h.service.UserPackages(ctx, p)
	respond(c, result, err)
}
func (h *Handler) walletTransactions(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	result, err := h.service.WalletTransactions(ctx, p, listParams(c))
	respond(c, result, err)
}
func (h *Handler) userBillingRecords(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	result, err := h.service.BillingRecords(ctx, p, listParams(c))
	respond(c, result, err)
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

func purchaseIdempotencyKey(c *hertzapp.RequestContext) string {
	return strings.TrimSpace(string(c.Request.Header.Peek("Idempotency-Key")))
}

// forceFlag 解析 ?force=1/true 的强制删除标记。
func forceFlag(c *hertzapp.RequestContext) bool {
	switch strings.ToLower(strings.TrimSpace(string(c.Query("force")))) {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}
func pathID(c *hertzapp.RequestContext) (int64, error) {
	id, err := strconv.ParseInt(strings.TrimSpace(c.Param("id")), 10, 64)
	if err != nil || id <= 0 {
		return 0, apperrors.New(consts.StatusBadRequest, "ID不合法")
	}
	return id, nil
}
func listParams(c *hertzapp.RequestContext) ListParams {
	params := ListParams{Keyword: string(c.Query("keyword")), Page: queryInt(c, "page", 1), PageSize: queryInt(c, "pageSize", 20), InterfaceID: queryInt64(c, "interfaceId", 0)}
	if raw := strings.TrimSpace(string(c.Query("status"))); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil {
			value = 2
		}
		params.Status = &value
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
