package rbac

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

type Handler struct {
	service *Service
}

func NewHandler(service *Service) *Handler {
	return &Handler{service: service}
}

func (h *Handler) RegisterMenu(group *route.RouterGroup) {
	group.GET("/all", h.userMenus)
}

func (h *Handler) RegisterSystem(group *route.RouterGroup) {
	group.GET("/menu/list", h.systemMenuList)
	group.GET("/menu/name-exists", h.menuNameExists)
	group.GET("/menu/path-exists", h.menuPathExists)
	group.POST("/menu", h.createMenu)
	group.PUT("/menu/:id", h.updateMenu)
	group.DELETE("/menu/:id", h.deleteMenu)

	group.GET("/role/list", h.roleList)
	group.GET("/role/options", h.roleOptions)
	group.POST("/role", h.createRole)
	group.PUT("/role/:id", h.updateRole)
	group.PATCH("/role/:id/status", h.updateRoleStatus)
	group.DELETE("/role/:id", h.deleteRole)
}

func (h *Handler) userMenus(ctx context.Context, c *hertzapp.RequestContext) {
	principal, err := h.principal(ctx, c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	result, err := h.service.UserMenus(ctx, principal)
	if err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, result)
}

func (h *Handler) systemMenuList(ctx context.Context, c *hertzapp.RequestContext) {
	principal, err := h.principal(ctx, c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	result, err := h.service.SystemMenuList(ctx, principal)
	if err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, result)
}

func (h *Handler) menuNameExists(ctx context.Context, c *hertzapp.RequestContext) {
	principal, err := h.principal(ctx, c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	id := queryInt64(c, "id", 0)
	result, err := h.service.MenuNameExists(ctx, principal, string(c.Query("name")), id)
	if err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, result)
}

func (h *Handler) menuPathExists(ctx context.Context, c *hertzapp.RequestContext) {
	principal, err := h.principal(ctx, c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	id := queryInt64(c, "id", 0)
	result, err := h.service.MenuPathExists(ctx, principal, string(c.Query("path")), id)
	if err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, result)
}

func (h *Handler) createMenu(ctx context.Context, c *hertzapp.RequestContext) {
	principal, err := h.principal(ctx, c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	var req SaveMenuRequest
	if err := bindJSON(c, &req); err != nil {
		response.FromError(c, err)
		return
	}
	if err := h.service.CreateMenu(ctx, principal, req); err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, true)
}

func (h *Handler) updateMenu(ctx context.Context, c *hertzapp.RequestContext) {
	principal, err := h.principal(ctx, c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	id, err := pathID(c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	var req SaveMenuRequest
	if err := bindJSON(c, &req); err != nil {
		response.FromError(c, err)
		return
	}
	if err := h.service.UpdateMenu(ctx, principal, id, req); err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, true)
}

func (h *Handler) deleteMenu(ctx context.Context, c *hertzapp.RequestContext) {
	principal, err := h.principal(ctx, c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	id, err := pathID(c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	if err := h.service.DeleteMenu(ctx, principal, id); err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, true)
}

func (h *Handler) roleList(ctx context.Context, c *hertzapp.RequestContext) {
	principal, err := h.principal(ctx, c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	params := RoleListParams{
		ID:       queryInt64(c, "id", 0),
		Name:     string(c.Query("name")),
		Page:     int(queryInt64(c, "page", 1)),
		PageSize: int(queryInt64(c, "pageSize", 20)),
		RoleKey:  string(c.Query("roleKey")),
	}
	if raw := strings.TrimSpace(string(c.Query("status"))); raw != "" {
		status, err := strconv.Atoi(raw)
		if err != nil {
			response.FromError(c, apperrors.New(consts.StatusBadRequest, "角色状态不合法"))
			return
		}
		params.Status = &status
	}
	if raw := strings.TrimSpace(string(c.Query("selfRegisterEnabled"))); raw != "" {
		enabled, err := strconv.Atoi(raw)
		if err != nil || (enabled != 0 && enabled != 1) {
			response.FromError(c, apperrors.New(consts.StatusBadRequest, "角色开放注册状态不合法"))
			return
		}
		params.SelfRegisterEnabled = &enabled
	}
	result, err := h.service.RoleList(ctx, principal, params)
	if err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, result)
}

func (h *Handler) roleOptions(ctx context.Context, c *hertzapp.RequestContext) {
	principal, err := h.principal(ctx, c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	result, err := h.service.RoleOptions(ctx, principal)
	if err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, result)
}

func (h *Handler) createRole(ctx context.Context, c *hertzapp.RequestContext) {
	principal, err := h.principal(ctx, c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	var req SaveRoleRequest
	if err := bindJSON(c, &req); err != nil {
		response.FromError(c, err)
		return
	}
	if err := h.service.CreateRole(ctx, principal, req); err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, true)
}

func (h *Handler) updateRole(ctx context.Context, c *hertzapp.RequestContext) {
	principal, err := h.principal(ctx, c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	id, err := pathID(c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	var req SaveRoleRequest
	if err := bindJSON(c, &req); err != nil {
		response.FromError(c, err)
		return
	}
	if err := h.service.UpdateRole(ctx, principal, id, req); err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, true)
}

func (h *Handler) deleteRole(ctx context.Context, c *hertzapp.RequestContext) {
	principal, err := h.principal(ctx, c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	id, err := pathID(c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	if err := h.service.DeleteRole(ctx, principal, id); err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, true)
}

func (h *Handler) updateRoleStatus(ctx context.Context, c *hertzapp.RequestContext) {
	principal, err := h.principal(ctx, c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	id, err := pathID(c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	var req UpdateRoleStatusRequest
	if err := bindJSON(c, &req); err != nil {
		response.FromError(c, err)
		return
	}
	if err := h.service.UpdateRoleStatus(ctx, principal, id, req.Status); err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, true)
}

func (h *Handler) principal(ctx context.Context, c *hertzapp.RequestContext) (Principal, error) {
	return h.service.Authenticate(ctx, bearerToken(c))
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

func queryInt64(c *hertzapp.RequestContext, key string, fallback int64) int64 {
	raw := strings.TrimSpace(string(c.Query(key)))
	if raw == "" {
		return fallback
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return fallback
	}
	return value
}
