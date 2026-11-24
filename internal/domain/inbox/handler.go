package inbox

import (
	"context"
	"strconv"
	"strings"

	"bugpk-api/internal/pkg/jsonrequest"
	"bugpk-api/internal/pkg/response"

	hertzapp "github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/route"
)

type Handler struct{ service *Service }

func NewHandler(service *Service) *Handler { return &Handler{service: service} }

// RegisterSystem 注册管理端路由（/system-api/inbox，随 systemGroup 获得审计中间件）。
func (h *Handler) RegisterSystem(group *route.RouterGroup) {
	group.POST("/broadcast", h.broadcast)
}

// RegisterUser 注册用户端路由（/system-api/user/inbox，登录态）。
func (h *Handler) RegisterUser(group *route.RouterGroup) {
	group.GET("", h.myMessages)
	group.GET("/unread-count", h.unreadCount)
	group.POST("/read", h.markRead)
}

type markReadRequest struct {
	IDs []int64 `json:"ids"`
}

func (h *Handler) broadcast(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	var req BroadcastRequest
	if !bind(c, &req) {
		return
	}
	result, err := h.service.Broadcast(ctx, p, req)
	respond(c, result, err)
}

func (h *Handler) myMessages(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	unreadOnly := queryFlag(c, "unread")
	page, _ := strconv.Atoi(strings.TrimSpace(c.Query("page")))
	pageSize, _ := strconv.Atoi(strings.TrimSpace(c.Query("pageSize")))
	result, err := h.service.MyMessages(ctx, p, unreadOnly, page, pageSize)
	respond(c, result, err)
}

func (h *Handler) unreadCount(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	count, err := h.service.UnreadCount(ctx, p)
	respond(c, map[string]any{"unread": count}, err)
}

func (h *Handler) markRead(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	var req markReadRequest
	if !bind(c, &req) {
		return
	}
	affected, err := h.service.MarkRead(ctx, p, req.IDs)
	respond(c, map[string]any{"updated": affected}, err)
}

func (h *Handler) principal(ctx context.Context, c *hertzapp.RequestContext) (Principal, bool) {
	p, err := h.service.Authenticate(ctx, bearer(c))
	if err != nil {
		response.FromError(c, err)
		return Principal{}, false
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

func queryFlag(c *hertzapp.RequestContext, key string) bool {
	switch strings.ToLower(strings.TrimSpace(c.Query(key))) {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}
