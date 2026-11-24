package user

import (
	"context"
	"strconv"
	"strings"

	"bugpk-api/internal/domain/auth"
	apperrors "bugpk-api/internal/pkg/errors"
	"bugpk-api/internal/pkg/jsonrequest"
	"bugpk-api/internal/pkg/response"

	hertzapp "github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"github.com/cloudwego/hertz/pkg/route"
)

type Handler struct {
	service *Service
}

func NewHandler(service *Service) *Handler {
	return &Handler{service: service}
}

func (h *Handler) RegisterUser(group *route.RouterGroup) {
	group.GET("/info", h.info)
	group.PUT("/profile", h.updateProfile)
	group.POST("/send-bind-email-code", h.sendBindEmailCode)
	group.PUT("/bind-email", h.bindEmail)
	group.POST("/send-bind-mobile-code", h.sendBindMobileCode)
	group.PUT("/bind-mobile", h.bindMobile)
	group.GET("/qq-binding", h.qqBindingStatus)
	group.GET("/qq-binding/authorize", h.qqBindingAuthorize)
	group.DELETE("/qq-binding", h.unbindQQ)
	group.PUT("/password", h.changePassword)
	group.POST("/password/set", h.setPassword)
	group.GET("/workbench/stats", h.userWorkbenchStats)
	group.GET("/workbench/admin-stats", h.adminWorkbenchStats)
}

func (h *Handler) RegisterSystem(group *route.RouterGroup) {
	group.GET("/user/list", h.listUsers)
	group.POST("/user", h.createUser)
	group.GET("/user/:id/specified-response", h.getSpecifiedResponse)
	group.PUT("/user/:id/specified-response", h.updateSpecifiedResponse)
	group.GET("/user/:id", h.getUser)
	group.PUT("/user/:id", h.updateUser)
	group.POST("/user/:id/adjust-assets", h.adjustUserAssets)
	group.DELETE("/user/:id", h.deleteUser)
}

func (h *Handler) info(ctx context.Context, c *hertzapp.RequestContext) {
	principal, err := h.principal(ctx, c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	result, err := h.service.UserInfo(ctx, principal)
	if err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, result)
}

func (h *Handler) updateProfile(ctx context.Context, c *hertzapp.RequestContext) {
	principal, err := h.principal(ctx, c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	var req UpdateProfileRequest
	if err := bindJSON(c, &req); err != nil {
		response.FromError(c, err)
		return
	}
	result, err := h.service.UpdateProfile(ctx, principal, req)
	if err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, result)
}

func (h *Handler) sendBindEmailCode(ctx context.Context, c *hertzapp.RequestContext) {
	principal, err := h.principal(ctx, c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	var req SendBindEmailCodeRequest
	if err := bindJSON(c, &req); err != nil {
		response.FromError(c, err)
		return
	}
	lang := auth.ResolveRequestEmailLang(
		string(c.Request.Header.Peek("X-App-Locale")),
		string(c.Request.Header.Peek("Accept-Language")),
	)
	if err := h.service.SendBindEmailCode(ctx, principal, req, c.ClientIP(), lang); err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, true)
}

func (h *Handler) bindEmail(ctx context.Context, c *hertzapp.RequestContext) {
	principal, err := h.principal(ctx, c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	var req BindEmailRequest
	if err := bindJSON(c, &req); err != nil {
		response.FromError(c, err)
		return
	}
	result, err := h.service.BindEmail(ctx, principal, req)
	if err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, result)
}

func (h *Handler) sendBindMobileCode(ctx context.Context, c *hertzapp.RequestContext) {
	principal, err := h.principal(ctx, c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	var req SendBindMobileCodeRequest
	if err := bindJSON(c, &req); err != nil {
		response.FromError(c, err)
		return
	}
	if err := h.service.SendBindMobileCode(ctx, principal, req, c.ClientIP()); err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, true)
}

func (h *Handler) bindMobile(ctx context.Context, c *hertzapp.RequestContext) {
	principal, err := h.principal(ctx, c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	var req BindMobileRequest
	if err := bindJSON(c, &req); err != nil {
		response.FromError(c, err)
		return
	}
	result, err := h.service.BindMobile(ctx, principal, req)
	if err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, result)
}

func (h *Handler) qqBindingStatus(ctx context.Context, c *hertzapp.RequestContext) {
	principal, err := h.principal(ctx, c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, h.service.QQBindingStatus(ctx, principal))
}

func (h *Handler) qqBindingAuthorize(ctx context.Context, c *hertzapp.RequestContext) {
	principal, err := h.principal(ctx, c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	result, err := h.service.QQBindingAuthorize(ctx, principal)
	if err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, result)
}

func (h *Handler) unbindQQ(ctx context.Context, c *hertzapp.RequestContext) {
	principal, err := h.principal(ctx, c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	result, err := h.service.UnbindQQ(ctx, principal)
	if err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, result)
}

func (h *Handler) changePassword(ctx context.Context, c *hertzapp.RequestContext) {
	principal, err := h.principal(ctx, c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	var req ChangePasswordRequest
	if err := bindJSON(c, &req); err != nil {
		response.FromError(c, err)
		return
	}
	if err := h.service.ChangePassword(ctx, principal, req, binding(c)); err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, true)
}

func (h *Handler) setPassword(ctx context.Context, c *hertzapp.RequestContext) {
	principal, err := h.principal(ctx, c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	var req SetPasswordRequest
	if err := bindJSON(c, &req); err != nil {
		response.FromError(c, err)
		return
	}
	if err := h.service.SetInitialPassword(ctx, principal, req, binding(c)); err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, true)
}

func (h *Handler) userWorkbenchStats(ctx context.Context, c *hertzapp.RequestContext) {
	principal, err := h.principal(ctx, c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	result, err := h.service.UserWorkbenchStats(ctx, principal)
	if err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, result)
}

func (h *Handler) adminWorkbenchStats(ctx context.Context, c *hertzapp.RequestContext) {
	principal, err := h.principal(ctx, c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	sections, err := ParseAdminWorkbenchSections(string(c.Query("sections")))
	if err != nil {
		response.FromError(c, apperrors.New(consts.StatusBadRequest, "invalid admin workbench sections"))
		return
	}
	// accessRange 只作用于访问态势图册；非法值由仓储层收敛为默认 30 天，
	// 这里不做 400，避免一个可选参数写错就让整块统计面板失败。
	accessRange := strings.TrimSpace(string(c.Query("accessRange")))
	result, err := h.service.AdminWorkbenchStatsWithAccessRange(ctx, principal, accessRange, sections...)
	if err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, result)
}

func (h *Handler) listUsers(ctx context.Context, c *hertzapp.RequestContext) {
	principal, err := h.principal(ctx, c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	var roleID int64
	if raw := strings.TrimSpace(string(c.Query("roleId"))); raw != "" {
		roleID, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || roleID <= 0 {
			response.FromError(c, apperrors.New(consts.StatusBadRequest, "角色ID不合法"))
			return
		}
	}
	params := UserListParams{
		IncludeSensitive: queryBool(c, "unmasked"),
		Keyword:          string(c.Query("keyword")),
		Page:             queryInt(c, "page", 1),
		PageSize:         queryInt(c, "pageSize", 20),
		RoleID:           roleID,
	}
	if raw := strings.TrimSpace(string(c.Query("status"))); raw != "" {
		status, err := strconv.Atoi(raw)
		if err != nil {
			response.FromError(c, apperrors.New(consts.StatusBadRequest, "用户状态不合法"))
			return
		}
		params.Status = &status
	}
	result, err := h.service.ListUsers(ctx, principal, params)
	if err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, result)
}

func (h *Handler) createUser(ctx context.Context, c *hertzapp.RequestContext) {
	principal, err := h.principal(ctx, c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	var req SaveUserRequest
	if err := bindJSON(c, &req); err != nil {
		response.FromError(c, err)
		return
	}
	if err := h.service.CreateUser(ctx, principal, req); err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, true)
}

func (h *Handler) getUser(ctx context.Context, c *hertzapp.RequestContext) {
	principal, err := h.principal(ctx, c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	userID, err := pathID(c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	result, err := h.service.GetSystemUser(ctx, principal, userID)
	if err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, result)
}

func (h *Handler) updateUser(ctx context.Context, c *hertzapp.RequestContext) {
	principal, err := h.principal(ctx, c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	userID, err := pathID(c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	var req SaveUserRequest
	if err := bindJSON(c, &req); err != nil {
		response.FromError(c, err)
		return
	}
	if err := h.service.UpdateUser(ctx, principal, userID, req); err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, true)
}

func (h *Handler) adjustUserAssets(ctx context.Context, c *hertzapp.RequestContext) {
	principal, err := h.principal(ctx, c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	userID, err := pathID(c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	var req AdjustUserAssetRequest
	if err := bindJSON(c, &req); err != nil {
		response.FromError(c, err)
		return
	}
	result, err := h.service.AdjustUserAssets(ctx, principal, userID, req)
	if err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, result)
}

func (h *Handler) deleteUser(ctx context.Context, c *hertzapp.RequestContext) {
	principal, err := h.principal(ctx, c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	userID, err := pathID(c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	if err := h.service.DeleteUser(ctx, principal, userID); err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, true)
}

func (h *Handler) getSpecifiedResponse(ctx context.Context, c *hertzapp.RequestContext) {
	principal, err := h.principal(ctx, c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	userID, err := pathID(c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	result, err := h.service.GetSpecifiedResponse(ctx, principal, userID)
	if err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, result)
}

func (h *Handler) updateSpecifiedResponse(ctx context.Context, c *hertzapp.RequestContext) {
	principal, err := h.principal(ctx, c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	userID, err := pathID(c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	var req SpecifiedResponseConfig
	if err := bindJSON(c, &req); err != nil {
		response.FromError(c, err)
		return
	}
	if err := h.service.UpdateSpecifiedResponse(ctx, principal, userID, req); err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, true)
}

func (h *Handler) principal(ctx context.Context, c *hertzapp.RequestContext) (Principal, error) {
	principal, _, err := h.service.Authenticate(ctx, bearerToken(c))
	return principal, err
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

func binding(c *hertzapp.RequestContext) string {
	return auth.CaptchaBinding(string(c.Request.Header.Peek("User-Agent")), string(c.Request.Header.Peek("Accept-Language")))
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
		return 0
	}
	return value
}

func queryBool(c *hertzapp.RequestContext, key string) bool {
	raw := strings.ToLower(strings.TrimSpace(string(c.Query(key))))
	return raw == "1" || raw == "true" || raw == "yes"
}
