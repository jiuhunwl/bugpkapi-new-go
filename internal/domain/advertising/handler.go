package advertising

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

func (h *Handler) Register(group *route.RouterGroup) {
	group.GET("/public", h.publicList)
	group.GET("/admin/list", h.adminList)
	group.POST("/admin", h.create)
	group.GET("/admin/:id", h.detail)
	group.PUT("/admin/:id", h.update)
	group.DELETE("/admin/:id", h.delete)
}

func (h *Handler) publicList(ctx context.Context, c *hertzapp.RequestContext) {
	result, err := h.service.PublicAds(ctx, string(c.Query("placement")))
	respond(c, result, err)
}

func (h *Handler) adminList(ctx context.Context, c *hertzapp.RequestContext) {
	principal, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	result, err := h.service.AdminList(ctx, principal, listParams(c))
	respond(c, result, err)
}

func (h *Handler) detail(ctx context.Context, c *hertzapp.RequestContext) {
	principal, id, ok := h.principalID(ctx, c)
	if !ok {
		return
	}
	result, err := h.service.AdminDetail(ctx, principal, id)
	respond(c, result, err)
}

func (h *Handler) create(ctx context.Context, c *hertzapp.RequestContext) {
	principal, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	var req SaveAdvertisementRequest
	if !bind(c, &req) {
		return
	}
	result, err := h.service.Create(ctx, principal, req)
	respond(c, result, err)
}

func (h *Handler) update(ctx context.Context, c *hertzapp.RequestContext) {
	principal, id, ok := h.principalID(ctx, c)
	if !ok {
		return
	}
	var req SaveAdvertisementRequest
	if !bind(c, &req) {
		return
	}
	respond(c, true, h.service.Update(ctx, principal, id, req))
}

func (h *Handler) delete(ctx context.Context, c *hertzapp.RequestContext) {
	principal, id, ok := h.principalID(ctx, c)
	if !ok {
		return
	}
	respond(c, true, h.service.Delete(ctx, principal, id))
}

func (h *Handler) principal(ctx context.Context, c *hertzapp.RequestContext) (Principal, bool) {
	principal, err := h.service.Authenticate(ctx, bearer(c))
	if err != nil {
		response.FromError(c, err)
		return Principal{}, false
	}
	return principal, true
}

func (h *Handler) principalID(ctx context.Context, c *hertzapp.RequestContext) (Principal, int64, bool) {
	principal, ok := h.principal(ctx, c)
	if !ok {
		return Principal{}, 0, false
	}
	id, err := strconv.ParseInt(strings.TrimSpace(c.Param("id")), 10, 64)
	if err != nil || id <= 0 {
		response.FromError(c, apperrors.New(consts.StatusBadRequest, "id不合法"))
		return Principal{}, 0, false
	}
	return principal, id, true
}

func listParams(c *hertzapp.RequestContext) ListParams {
	params := ListParams{
		Keyword:   strings.TrimSpace(string(c.Query("keyword"))),
		Page:      queryInt(c, "page", 1),
		PageSize:  queryInt(c, "pageSize", 20),
		Placement: strings.TrimSpace(string(c.Query("placement"))),
	}
	if raw := strings.TrimSpace(string(c.Query("status"))); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil {
			value = -1
		}
		params.Status = &value
	}
	return params
}

func queryInt(c *hertzapp.RequestContext, name string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(string(c.Query(name))))
	if err != nil {
		return fallback
	}
	return value
}

func bind(c *hertzapp.RequestContext, target any) bool {
	if err := jsonrequest.BindHertz(c, target, jsonrequest.DefaultMaxBodyBytes); err != nil {
		response.FromError(c, err)
		return false
	}
	return true
}

func respond(c *hertzapp.RequestContext, data any, err error) {
	if err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, data)
}

func bearer(c *hertzapp.RequestContext) string {
	value := string(c.Request.Header.Peek("Authorization"))
	if strings.HasPrefix(strings.ToLower(value), "bearer ") {
		return strings.TrimSpace(value[7:])
	}
	return ""
}
