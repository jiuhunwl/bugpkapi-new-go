package riskcontrol

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"bugpk-api/internal/domain/rbac"
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
	group.GET("/overview", h.overview)
	group.GET("/events", h.events)
	group.GET("/earth", h.earth)
	group.POST("/accounts/ban", h.banAccount)
}

func (h *Handler) overview(ctx context.Context, c *hertzapp.RequestContext) {
	principal, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	query, err := riskQuery(c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	result, err := h.service.Overview(ctx, principal, query)
	respond(c, result, err)
}

func (h *Handler) events(ctx context.Context, c *hertzapp.RequestContext) {
	principal, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	query, err := riskQuery(c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	result, err := h.service.Events(ctx, principal, query)
	respond(c, result, err)
}

func (h *Handler) earth(ctx context.Context, c *hertzapp.RequestContext) {
	principal, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	query, err := riskQuery(c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	result, err := h.service.Earth(ctx, principal, query)
	respond(c, result, err)
}

func (h *Handler) banAccount(ctx context.Context, c *hertzapp.RequestContext) {
	principal, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	var req BanAccountRequest
	if err := jsonrequest.BindHertz(c, &req, jsonrequest.DefaultMaxBodyBytes); err != nil {
		response.FromError(c, err)
		return
	}
	if err := h.service.BanAccount(ctx, principal, req); err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, true)
}

func (h *Handler) principal(ctx context.Context, c *hertzapp.RequestContext) (rbac.Principal, bool) {
	principal, err := h.service.Authenticate(ctx, bearerToken(c))
	if err != nil {
		response.FromError(c, err)
		return rbac.Principal{}, false
	}
	return principal, true
}

func riskQuery(c *hertzapp.RequestContext) (RiskQuery, error) {
	limit, err := boundedRiskInt(string(c.Query("limit")), "limit", defaultLimit, 1, maxLimit)
	if err != nil {
		return RiskQuery{}, err
	}
	windowMinutes, err := boundedRiskInt(string(c.Query("windowMinutes")), "windowMinutes", defaultWindowMinutes, 1, maxWindowMinutes)
	if err != nil {
		return RiskQuery{}, err
	}
	return RiskQuery{Limit: limit, WindowMinutes: windowMinutes}, nil
}

func respond(c *hertzapp.RequestContext, data any, err error) {
	if err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, data)
}

func bearerToken(c *hertzapp.RequestContext) string {
	value := string(c.Request.Header.Peek("Authorization"))
	if strings.HasPrefix(strings.ToLower(value), "bearer ") {
		return strings.TrimSpace(value[7:])
	}
	return ""
}

func boundedRiskInt(raw, key string, fallback, min, max int) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < min || value > max {
		return 0, apperrors.New(consts.StatusBadRequest, fmt.Sprintf("%s must be between %d and %d", key, min, max))
	}
	return value, nil
}
