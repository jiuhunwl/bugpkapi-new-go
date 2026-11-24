package translation

import (
	"context"
	"strconv"
	"strings"

	"bugpk-api/internal/pkg/jsonrequest"
	"bugpk-api/internal/pkg/response"

	hertzapp "github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/route"
)

type Handler struct {
	service *Service
}

func NewHandler(service *Service) *Handler {
	return &Handler{service: service}
}

func (h *Handler) Register(group *route.RouterGroup) {
	group.GET("/locales", h.locales)
	group.PUT("/locales", h.saveLocales)
	group.GET("/resource-types", h.resourceTypes)
	group.GET("/resources", h.listResources)
	group.GET("/resources/:resourceType/:resourceKey", h.getResource)
	group.PUT("/resources/:resourceType/:resourceKey", h.saveTranslations)
}

func (h *Handler) locales(ctx context.Context, c *hertzapp.RequestContext) {
	result, err := h.service.Locales(ctx, false)
	respond(c, result, err)
}

func (h *Handler) saveLocales(ctx context.Context, c *hertzapp.RequestContext) {
	principal, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	var req SaveLocalesRequest
	if !bind(c, &req) {
		return
	}
	result, err := h.service.SaveLocales(ctx, principal, req)
	respond(c, result, err)
}

func (h *Handler) resourceTypes(ctx context.Context, c *hertzapp.RequestContext) {
	principal, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	result, err := h.service.ResourceTypes(ctx, principal)
	respond(c, result, err)
}

func (h *Handler) listResources(ctx context.Context, c *hertzapp.RequestContext) {
	principal, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	result, err := h.service.ListResources(
		ctx,
		principal,
		strings.TrimSpace(string(c.Query("resourceType"))),
		strings.TrimSpace(string(c.Query("keyword"))),
		queryInt(c, "page", 1),
		queryInt(c, "pageSize", 20),
	)
	respond(c, result, err)
}

func (h *Handler) getResource(ctx context.Context, c *hertzapp.RequestContext) {
	principal, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	result, err := h.service.GetResource(
		ctx,
		principal,
		strings.TrimSpace(c.Param("resourceType")),
		strings.TrimSpace(c.Param("resourceKey")),
	)
	respond(c, result, err)
}

func (h *Handler) saveTranslations(ctx context.Context, c *hertzapp.RequestContext) {
	principal, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	var req SaveTranslationsRequest
	if !bind(c, &req) {
		return
	}
	result, err := h.service.SaveTranslations(
		ctx,
		principal,
		strings.TrimSpace(c.Param("resourceType")),
		strings.TrimSpace(c.Param("resourceKey")),
		req,
	)
	respond(c, result, err)
}

func (h *Handler) principal(ctx context.Context, c *hertzapp.RequestContext) (Principal, bool) {
	principal, err := h.service.Authenticate(ctx, bearer(c))
	if err != nil {
		response.FromError(c, err)
		return Principal{}, false
	}
	return principal, true
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

func queryInt(c *hertzapp.RequestContext, name string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(string(c.Query(name))))
	if err != nil {
		return fallback
	}
	return value
}

func bearer(c *hertzapp.RequestContext) string {
	value := string(c.Request.Header.Peek("Authorization"))
	if strings.HasPrefix(strings.ToLower(value), "bearer ") {
		return strings.TrimSpace(value[7:])
	}
	return ""
}
