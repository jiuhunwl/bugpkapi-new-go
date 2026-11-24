package gateway

import (
	"bytes"
	"context"
	"net/url"
	"strconv"
	"strings"

	"bugpk-api/internal/domain/rbac"
	apperrors "bugpk-api/internal/pkg/errors"
	"bugpk-api/internal/pkg/jsonrequest"
	"bugpk-api/internal/pkg/requestid"
	"bugpk-api/internal/pkg/response"

	hertzapp "github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"github.com/cloudwego/hertz/pkg/route"
)

type Handler struct {
	service *Service
}

const (
	maxOpenHeaderBytes      = 32 * 1024
	maxOpenHeaderCount      = 100
	maxOpenJSONNestingDepth = 64
	maxOpenRequestBodyBytes = 2 * 1024 * 1024
	maxOpenQueryBytes       = 16 * 1024
	maxOpenQueryNameBytes   = 128
	maxOpenQueryValueBytes  = 8 * 1024
	maxOpenQueryValueCount  = 100
)

func NewHandler(service *Service) *Handler {
	return &Handler{service: service}
}

func (h *Handler) RegisterKey(group *route.RouterGroup) {
	group.GET("/list", h.keyList)
	group.POST("", h.createKey)
	group.PUT("/:id/status", h.updateStatus)
	group.GET("/:id/interfaces", h.interfaces)
	group.PUT("/:id/interfaces", h.replaceInterfaces)
	group.GET("/:id/ip-whitelist", h.ipWhitelist)
	group.PUT("/:id/ip-whitelist", h.replaceIPWhitelist)
	group.GET("/:id/secret", h.keySecret)
	group.GET("/:id", h.keyDetail)
	group.PUT("/:id", h.updateKey)
	group.DELETE("/:id", h.deleteKey)
}

func (h *Handler) RegisterOpen(group *route.RouterGroup) {
	group.Any("/:apiCode", h.open)
}

// RegisterUserQuota 挂用户侧的额度概览（只读）。
// 与 RegisterKey 共用 /user 前缀，但走 JWT 身份而不是接口 Key，所以单独注册。
func (h *Handler) RegisterUserQuota(group *route.RouterGroup) {
	group.GET("/quota/overview", h.quotaOverview)
}

func (h *Handler) quotaOverview(ctx context.Context, c *hertzapp.RequestContext) {
	principal, err := h.principal(ctx, c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	result, err := h.service.QuotaOverview(ctx, principal.ID)
	if err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, result)
}

func (h *Handler) keyList(ctx context.Context, c *hertzapp.RequestContext) {
	principal, err := h.principal(ctx, c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	params := KeyListParams{
		Keyword:    string(c.Query("keyword")),
		KeyType:    strings.ToUpper(strings.TrimSpace(string(c.Query("keyType")))),
		OwnerScope: keyListOwnerScopeQuery(c),
		Page:       queryInt(c, "page", 1),
		PageSize:   queryInt(c, "pageSize", 20),
	}
	if raw := strings.TrimSpace(string(c.Query("status"))); raw != "" {
		status, err := strconv.Atoi(raw)
		if err != nil {
			response.FromError(c, apperrors.New(consts.StatusBadRequest, "Key状态不合法"))
			return
		}
		params.Status = &status
	}
	result, err := h.service.ListKeys(ctx, principal, params)
	if err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, result)
}

func (h *Handler) keyDetail(ctx context.Context, c *hertzapp.RequestContext) {
	principal, id, ok := h.principalAndID(ctx, c)
	if !ok {
		return
	}
	result, err := h.service.KeyDetail(ctx, principal, id)
	if err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, result)
}

func (h *Handler) keySecret(ctx context.Context, c *hertzapp.RequestContext) {
	principal, id, ok := h.principalAndID(ctx, c)
	if !ok {
		return
	}
	result, err := h.service.KeySecret(ctx, principal, id)
	if err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, result)
}

func (h *Handler) createKey(ctx context.Context, c *hertzapp.RequestContext) {
	principal, err := h.principal(ctx, c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	var req SaveKeyRequest
	if err := bindJSON(c, &req); err != nil {
		response.FromError(c, err)
		return
	}
	result, err := h.service.CreateKey(ctx, principal, req)
	if err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, result)
}

func (h *Handler) updateKey(ctx context.Context, c *hertzapp.RequestContext) {
	principal, id, ok := h.principalAndID(ctx, c)
	if !ok {
		return
	}
	var req SaveKeyRequest
	if err := bindJSON(c, &req); err != nil {
		response.FromError(c, err)
		return
	}
	if err := h.service.UpdateKey(ctx, principal, id, req); err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, true)
}

func (h *Handler) deleteKey(ctx context.Context, c *hertzapp.RequestContext) {
	principal, id, ok := h.principalAndID(ctx, c)
	if !ok {
		return
	}
	if err := h.service.DeleteKey(ctx, principal, id); err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, true)
}

func (h *Handler) updateStatus(ctx context.Context, c *hertzapp.RequestContext) {
	principal, id, ok := h.principalAndID(ctx, c)
	if !ok {
		return
	}
	var req StatusRequest
	if err := bindJSON(c, &req); err != nil {
		response.FromError(c, err)
		return
	}
	if err := h.service.UpdateStatus(ctx, principal, id, req.Status); err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, true)
}

func (h *Handler) interfaces(ctx context.Context, c *hertzapp.RequestContext) {
	principal, id, ok := h.principalAndID(ctx, c)
	if !ok {
		return
	}
	result, err := h.service.Interfaces(ctx, principal, id)
	if err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, map[string]any{"interfaceIds": result})
}

func (h *Handler) replaceInterfaces(ctx context.Context, c *hertzapp.RequestContext) {
	principal, id, ok := h.principalAndID(ctx, c)
	if !ok {
		return
	}
	var req InterfaceIDsRequest
	if err := bindJSON(c, &req); err != nil {
		response.FromError(c, err)
		return
	}
	if err := h.service.ReplaceInterfaces(ctx, principal, id, req); err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, true)
}

func (h *Handler) ipWhitelist(ctx context.Context, c *hertzapp.RequestContext) {
	principal, id, ok := h.principalAndID(ctx, c)
	if !ok {
		return
	}
	result, err := h.service.IPRules(ctx, principal, id)
	if err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, map[string]any{"ipWhitelist": result})
}

func (h *Handler) replaceIPWhitelist(ctx context.Context, c *hertzapp.RequestContext) {
	principal, id, ok := h.principalAndID(ctx, c)
	if !ok {
		return
	}
	var req IPWhitelistRequest
	if err := bindJSON(c, &req); err != nil {
		response.FromError(c, err)
		return
	}
	if err := h.service.ReplaceIPRules(ctx, principal, id, req); err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, true)
}

func (h *Handler) open(ctx context.Context, c *hertzapp.RequestContext) {
	ctx = requestid.Ensure(ctx)
	id := requestid.FromContext(ctx)
	c.Header(requestid.Header, id)
	defer func() {
		c.Header(requestid.Header, id)
		if string(c.Method()) != "HEAD" && c.Response.StatusCode() != 204 && c.Response.StatusCode() != 304 {
			c.Response.SetBody(requestid.Decorate(c.Response.Body(), string(c.Response.Header.ContentType()), id))
		}
	}()
	req, err := openRequest(c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	result, err := h.service.Open(ctx, c.Param("apiCode"), req)
	if err != nil {
		response.FromError(c, err)
		return
	}
	contentType := strings.TrimSpace(result.ContentType)
	if contentType == "" {
		contentType = "application/json; charset=utf-8"
	}
	if result.CacheHit {
		c.Header("X-Bugpk-Cache", "HIT")
	}
	if result.Degraded {
		c.Header("X-Bugpk-Degraded", "1")
	}
	c.Data(result.ResponseStatus, contentType, []byte(result.Body))
}

func (h *Handler) principalAndID(ctx context.Context, c *hertzapp.RequestContext) (rbac.Principal, int64, bool) {
	principal, err := h.principal(ctx, c)
	if err != nil {
		response.FromError(c, err)
		return rbac.Principal{}, 0, false
	}
	id, err := pathID(c)
	if err != nil {
		response.FromError(c, err)
		return rbac.Principal{}, 0, false
	}
	return principal, id, true
}

func (h *Handler) principal(ctx context.Context, c *hertzapp.RequestContext) (rbac.Principal, error) {
	return h.service.Authenticate(ctx, bearerToken(c))
}

func openRequest(c *hertzapp.RequestContext) (OpenRequest, error) {
	if contentLength := c.Request.Header.ContentLength(); contentLength > maxOpenRequestBodyBytes {
		return OpenRequest{}, apperrors.New(consts.StatusRequestEntityTooLarge, "请求体不能超过2 MiB")
	}
	body, err := c.Body()
	if err != nil {
		return OpenRequest{}, apperrors.Wrap(consts.StatusBadRequest, "请求参数不合法", err)
	}
	if len(body) > maxOpenRequestBodyBytes {
		return OpenRequest{}, apperrors.New(consts.StatusRequestEntityTooLarge, "请求体不能超过2 MiB")
	}
	if len(bytes.TrimSpace(body)) > 0 && isJSONContentType(string(c.Request.Header.ContentType())) {
		if _, err := decodeUnambiguousJSON(body); err != nil {
			return OpenRequest{}, apperrors.Wrap(consts.StatusBadRequest, "JSON请求体格式不合法", err)
		}
	}
	rawQuery := string(c.URI().QueryString())
	if len(rawQuery) > maxOpenQueryBytes {
		return OpenRequest{}, apperrors.New(consts.StatusRequestEntityTooLarge, "Query 参数过长")
	}
	query, err := url.ParseQuery(rawQuery)
	if err != nil {
		return OpenRequest{}, apperrors.Wrap(consts.StatusBadRequest, "Query 参数格式不合法", err)
	}
	if err := validateOpenQuery(query); err != nil {
		return OpenRequest{}, err
	}
	headers := map[string]string{}
	headerCount := 0
	headerBytes := 0
	apiKeyHeaderCount := 0
	c.Request.Header.VisitAll(func(key, value []byte) {
		headerCount++
		headerBytes += len(key) + len(value)
		if strings.EqualFold(string(key), "X-API-Key") {
			apiKeyHeaderCount++
		}
		headers[string(key)] = string(value)
	})
	if headerCount > maxOpenHeaderCount || headerBytes > maxOpenHeaderBytes {
		return OpenRequest{}, apperrors.New(consts.StatusRequestHeaderFieldsTooLarge, "请求头过多或过长")
	}
	if apiKeyHeaderCount > 1 {
		return OpenRequest{}, apperrors.New(consts.StatusBadRequest, "X-API-Key请求头不能重复")
	}
	return OpenRequest{
		Body:      body,
		ClientIP:  c.ClientIP(),
		Headers:   headers,
		Method:    string(c.Method()),
		Query:     query,
		RawQuery:  rawQuery,
		UserAgent: string(c.UserAgent()),
	}, nil
}

func isJSONContentType(contentType string) bool {
	mediaType := strings.ToLower(strings.TrimSpace(strings.SplitN(contentType, ";", 2)[0]))
	return mediaType == "application/json" || strings.HasSuffix(mediaType, "+json")
}

func validateOpenQuery(query url.Values) error {
	valueCount := 0
	for name, values := range query {
		if strings.TrimSpace(name) == "" || len(name) > maxOpenQueryNameBytes {
			return apperrors.New(consts.StatusBadRequest, "Query 参数名不合法")
		}
		valueCount += len(values)
		if valueCount > maxOpenQueryValueCount {
			return apperrors.New(consts.StatusBadRequest, "Query 参数数量不能超过100个")
		}
		for _, value := range values {
			if len(value) > maxOpenQueryValueBytes {
				return apperrors.New(consts.StatusRequestEntityTooLarge, "Query 参数值过长")
			}
		}
	}
	return nil
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

// keyListOwnerScopeQuery reads the canonical scope parameter and accepts the
// two names used by older frontend builds while they are being upgraded. The
// aliases are normalized by normalizeKeyListParams before authorization.
func keyListOwnerScopeQuery(c *hertzapp.RequestContext) string {
	for _, name := range []string{"scope", "ownerScope", "userScope"} {
		if value := strings.TrimSpace(string(c.Query(name))); value != "" {
			return value
		}
	}
	return ""
}
