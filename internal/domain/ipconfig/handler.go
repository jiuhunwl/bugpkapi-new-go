package ipconfig

import (
	"context"
	"strings"

	"bugpk-api/internal/domain/rbac"
	"bugpk-api/internal/pkg/jsonrequest"
	"bugpk-api/internal/pkg/response"

	hertzapp "github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/route"
)

// Handler 暴露 IP 数据源配置的后台管理接口。
type Handler struct {
	service *Service
}

func NewHandler(service *Service) *Handler {
	return &Handler{service: service}
}

func (h *Handler) RegisterSystem(group *route.RouterGroup) {
	group.GET("/ip-config", h.config)
	group.PUT("/ip-config", h.saveConfig)
	group.POST("/ip-config/test-source", h.testSource)
}

func (h *Handler) testSource(ctx context.Context, c *hertzapp.RequestContext) {
	principal, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	var req TestSourceRequest
	if err := jsonrequest.BindHertz(c, &req, 64*1024); err != nil {
		response.FromError(c, err)
		return
	}
	result, err := h.service.TestSource(ctx, principal, req)
	respond(c, result, err)
}

func (h *Handler) config(ctx context.Context, c *hertzapp.RequestContext) {
	principal, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	result, err := h.service.Config(ctx, principal)
	respond(c, result, err)
}

func (h *Handler) saveConfig(ctx context.Context, c *hertzapp.RequestContext) {
	principal, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	var req Config
	if err := jsonrequest.BindHertz(c, &req, jsonrequest.DefaultMaxBodyBytes); err != nil {
		response.FromError(c, err)
		return
	}
	respond(c, true, h.service.SaveConfig(ctx, principal, req))
}

func (h *Handler) principal(ctx context.Context, c *hertzapp.RequestContext) (rbac.Principal, bool) {
	p, err := h.service.Authenticate(ctx, bearer(c))
	if err != nil {
		response.FromError(c, err)
		return rbac.Principal{}, false
	}
	return p, true
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
