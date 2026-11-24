package auth

import (
	"context"
	"errors"
	"net/url"
	"strings"

	"bugpk-api/internal/config"
	apperrors "bugpk-api/internal/pkg/errors"
	"bugpk-api/internal/pkg/response"

	hertzapp "github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"github.com/cloudwego/hertz/pkg/route"
)

type OAuthHandler struct {
	cfg     config.Config
	service *OAuthService
}

func NewOAuthHandler(cfg config.Config, service *OAuthService) *OAuthHandler {
	return &OAuthHandler{cfg: cfg, service: service}
}

func (h *OAuthHandler) RegisterPublic(group *route.RouterGroup) {
	group.GET("/providers", h.providers)
	group.POST("/:provider/authorize", h.authorize)
	group.GET("/:provider/callback", h.callback)
	group.POST("/exchange", h.exchange)
	group.GET("/complete", h.pendingInfo)
	group.POST("/complete", h.complete)
}

func (h *OAuthHandler) RegisterUser(group *route.RouterGroup) {
	group.GET("/oauth-bindings", h.bindings)
	group.POST("/oauth-bindings/:provider/authorize", h.bindAuthorize)
	group.POST("/oauth-bindings/:provider/unbind", h.unbind)
}

func (h *OAuthHandler) RegisterSystem(group *route.RouterGroup) {
	group.GET("/login/oauth-config", h.adminConfig)
	group.PUT("/login/oauth-config", h.adminSaveGlobal)
	group.POST("/login/oauth-config/providers", h.adminCreateProvider)
	group.PUT("/login/oauth-config/providers/:provider", h.adminUpdateProvider)
	group.POST("/login/oauth-config/providers/:provider/test", h.adminTestProvider)
}

func (h *OAuthHandler) providers(ctx context.Context, c *hertzapp.RequestContext) {
	result, err := h.service.PublicProviders(ctx)
	if err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, result)
}

func (h *OAuthHandler) authorize(ctx context.Context, c *hertzapp.RequestContext) {
	var req OAuthAuthorizeRequest
	if err := bindJSON(c, &req); err != nil {
		response.FromError(c, err)
		return
	}
	if strings.TrimSpace(req.Mode) == "" {
		req.Mode = OAuthModeLogin
	}
	if req.Mode != OAuthModeLogin {
		response.FromError(c, apperrors.New(consts.StatusBadRequest, "授权模式不合法"))
		return
	}
	url, flowID, err := h.service.Authorize(ctx, c.Param("provider"), req.Mode, req.ReturnPath, 0, c.ClientIP(), header(c, "User-Agent"))
	if err != nil {
		response.FromError(c, oauthAPIError(err))
		return
	}
	h.setFlowCookie(c, flowID)
	response.OK(c, OAuthAuthorizeResult{URL: url})
}

func (h *OAuthHandler) callback(ctx context.Context, c *hertzapp.RequestContext) {
	result, err := h.service.Callback(
		ctx,
		c.Param("provider"),
		string(c.Query("code")),
		string(c.Query("state")),
		string(c.Query("error")),
		h.flowID(c),
		c.ClientIP(),
		header(c, "User-Agent"),
	)
	if err != nil {
		h.clearFlowCookie(c)
		code := OAuthErrorProviderError
		reason := ""
		var flowErr *OAuthFlowError
		if errors.As(err, &flowErr) {
			code = flowErr.Code
			reason = flowErr.Detail
		}
		target := "/oauth/callback?error=" + url.QueryEscape(code)
		if reason != "" {
			target += "&reason=" + url.QueryEscape(reason)
		}
		h.redirectTo(ctx, c, target)
		return
	}
	if result.ClearFlow {
		h.clearFlowCookie(c)
	}
	h.redirectTo(ctx, c, result.RedirectPath)
}

func (h *OAuthHandler) exchange(ctx context.Context, c *hertzapp.RequestContext) {
	if err := requireSameOriginCookieRequest(c); err != nil {
		response.FromError(c, err)
		return
	}
	var req OAuthExchangeRequest
	if err := bindJSON(c, &req); err != nil {
		response.FromError(c, err)
		return
	}
	result, err := h.service.Exchange(ctx, req.Ticket, h.flowID(c), c.ClientIP(), header(c, "User-Agent"))
	if err != nil {
		h.clearFlowCookie(c)
		response.FromError(c, oauthAPIError(err))
		return
	}
	h.clearFlowCookie(c)
	h.writeRefreshCookie(c, result.RefreshToken)
	response.OK(c, result)
}

func (h *OAuthHandler) pendingInfo(ctx context.Context, c *hertzapp.RequestContext) {
	result, err := h.service.PendingInfo(ctx, string(c.Query("ticket")), h.flowID(c), c.ClientIP())
	if err != nil {
		response.FromError(c, oauthAPIError(err))
		return
	}
	response.OK(c, result)
}

func (h *OAuthHandler) complete(ctx context.Context, c *hertzapp.RequestContext) {
	if err := requireSameOriginCookieRequest(c); err != nil {
		response.FromError(c, err)
		return
	}
	var req OAuthCompleteRequest
	if err := bindJSON(c, &req); err != nil {
		response.FromError(c, err)
		return
	}
	result, err := h.service.Complete(ctx, req, h.flowID(c), c.ClientIP(), header(c, "User-Agent"))
	if err != nil {
		if shouldClearFlowOnComplete(err) {
			h.clearFlowCookie(c)
		}
		response.FromError(c, oauthAPIError(err))
		return
	}
	h.clearFlowCookie(c)
	h.writeRefreshCookie(c, result.RefreshToken)
	response.OK(c, result)
}

func (h *OAuthHandler) bindings(ctx context.Context, c *hertzapp.RequestContext) {
	user, err := h.currentUser(ctx, c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	result, err := h.service.Bindings(ctx, user.ID)
	if err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, result)
}

func (h *OAuthHandler) bindAuthorize(ctx context.Context, c *hertzapp.RequestContext) {
	user, err := h.currentUser(ctx, c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	url, flowID, err := h.service.Authorize(ctx, c.Param("provider"), OAuthModeBind, "", user.ID, c.ClientIP(), header(c, "User-Agent"))
	if err != nil {
		response.FromError(c, oauthAPIError(err))
		return
	}
	h.setFlowCookie(c, flowID)
	response.OK(c, OAuthAuthorizeResult{URL: url})
}

func (h *OAuthHandler) unbind(ctx context.Context, c *hertzapp.RequestContext) {
	user, err := h.currentUser(ctx, c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	var req OAuthUnbindRequest
	if err := bindJSON(c, &req); err != nil {
		response.FromError(c, err)
		return
	}
	if err := h.service.Unbind(ctx, user.ID, c.Param("provider"), req, c.ClientIP(), header(c, "User-Agent")); err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, true)
}

func (h *OAuthHandler) adminConfig(ctx context.Context, c *hertzapp.RequestContext) {
	if _, err := h.requireAdmin(ctx, c, "system:login:oauth-config"); err != nil {
		response.FromError(c, err)
		return
	}
	result, err := h.service.AdminList(ctx)
	if err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, result)
}

func (h *OAuthHandler) adminSaveGlobal(ctx context.Context, c *hertzapp.RequestContext) {
	if _, err := h.requireAdmin(ctx, c, "system:login:oauth-config:update"); err != nil {
		response.FromError(c, err)
		return
	}
	var req OAuthGlobalConfigSaveRequest
	if err := bindJSON(c, &req); err != nil {
		response.FromError(c, err)
		return
	}
	result, err := h.service.AdminSaveGlobal(ctx, req)
	if err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, result)
}

func (h *OAuthHandler) adminCreateProvider(ctx context.Context, c *hertzapp.RequestContext) {
	if _, err := h.requireAdmin(ctx, c, "system:login:oauth-config:update"); err != nil {
		response.FromError(c, err)
		return
	}
	var req OAuthProviderSaveRequest
	if err := bindJSON(c, &req); err != nil {
		response.FromError(c, err)
		return
	}
	result, err := h.service.AdminCreateProvider(ctx, req)
	if err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, result)
}

func (h *OAuthHandler) adminUpdateProvider(ctx context.Context, c *hertzapp.RequestContext) {
	if _, err := h.requireAdmin(ctx, c, "system:login:oauth-config:update"); err != nil {
		response.FromError(c, err)
		return
	}
	var req OAuthProviderSaveRequest
	if err := bindJSON(c, &req); err != nil {
		response.FromError(c, err)
		return
	}
	result, err := h.service.AdminUpdateProvider(ctx, c.Param("provider"), req)
	if err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, result)
}

func (h *OAuthHandler) adminTestProvider(ctx context.Context, c *hertzapp.RequestContext) {
	if _, err := h.requireAdmin(ctx, c, "system:login:oauth-config:update"); err != nil {
		response.FromError(c, err)
		return
	}
	result, err := h.service.AdminTestProvider(ctx, c.Param("provider"))
	if err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, result)
}

func (h *OAuthHandler) currentUser(ctx context.Context, c *hertzapp.RequestContext) (User, error) {
	return h.service.auth.UserFromAccessToken(ctx, bearerToken(c))
}

func (h *OAuthHandler) requireAdmin(ctx context.Context, c *hertzapp.RequestContext, code string) (User, error) {
	user, err := h.currentUser(ctx, c)
	if err != nil {
		return User{}, err
	}
	if h.service.adminChecker == nil {
		return User{}, apperrors.New(consts.StatusForbidden, "Forbidden Exception")
	}
	allowed, err := h.service.adminChecker.HasPermission(ctx, user.ID, user.Roles, code)
	if err != nil {
		return User{}, apperrors.Wrap(consts.StatusServiceUnavailable, "权限校验暂不可用", err)
	}
	if !allowed {
		return User{}, apperrors.New(consts.StatusForbidden, "Forbidden Exception")
	}
	return user, nil
}

func (h *OAuthHandler) redirectTo(ctx context.Context, c *hertzapp.RequestContext, path string) {
	base, err := h.service.FrontendBaseURL(ctx)
	if err != nil {
		response.FromError(c, apperrors.Wrap(consts.StatusServiceUnavailable, "第三方登录配置暂不可用", err))
		return
	}
	if base == "" {
		response.FromError(c, apperrors.New(consts.StatusInternalServerError, "第三方登录前端地址未配置"))
		return
	}
	target := strings.TrimRight(base, "/") + path
	c.Redirect(consts.StatusFound, []byte(target))
}

func (h *OAuthHandler) flowCookiePath() string {
	base := strings.TrimRight(strings.TrimSpace(h.cfg.API.SystemPrefix), "/")
	if base == "" {
		return "/auth/oauth"
	}
	return base + "/auth/oauth"
}

func (h *OAuthHandler) setFlowCookie(c *hertzapp.RequestContext, flowID string) {
	cookie := protocol.AcquireCookie()
	defer protocol.ReleaseCookie(cookie)
	cookie.SetKey(oauthFlowCookieName)
	cookie.SetValue(url.QueryEscape(flowID))
	cookie.SetMaxAge(int(oauthCookieTTL.Seconds()))
	cookie.SetPath(h.flowCookiePath())
	cookie.SetSameSite(sameSite(h.cfg.Auth.RefreshCookieSameSite))
	cookie.SetSecure(h.cfg.Auth.RefreshCookieSecure)
	cookie.SetHTTPOnly(true)
	c.Response.Header.Add("Set-Cookie", string(cookie.Cookie()))
}

func (h *OAuthHandler) clearFlowCookie(c *hertzapp.RequestContext) {
	cookie := protocol.AcquireCookie()
	defer protocol.ReleaseCookie(cookie)
	cookie.SetKey(oauthFlowCookieName)
	cookie.SetValue("")
	cookie.SetMaxAge(-1)
	cookie.SetPath(h.flowCookiePath())
	cookie.SetSameSite(sameSite(h.cfg.Auth.RefreshCookieSameSite))
	cookie.SetSecure(h.cfg.Auth.RefreshCookieSecure)
	cookie.SetHTTPOnly(true)
	c.Response.Header.Add("Set-Cookie", string(cookie.Cookie()))
}

func (h *OAuthHandler) flowID(c *hertzapp.RequestContext) string {
	raw := string(c.Cookie(oauthFlowCookieName))
	if raw == "" {
		raw = cookieValueFromHeader(header(c, "Cookie"), oauthFlowCookieName)
	}
	if decoded, err := url.QueryUnescape(raw); err == nil {
		raw = decoded
	}
	return strings.TrimSpace(raw)
}

func (h *OAuthHandler) writeRefreshCookie(c *hertzapp.RequestContext, refreshToken string) {
	if strings.TrimSpace(refreshToken) == "" {
		return
	}
	cookieWriter := &Handler{cfg: h.cfg}
	cookieWriter.setRefreshCookie(c, refreshToken, int(h.service.auth.RefreshTTL().Seconds()))
}

func cookieValueFromHeader(raw, name string) string {
	for _, part := range strings.Split(raw, ";") {
		key, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(key), name) {
			continue
		}
		return strings.Trim(strings.TrimSpace(value), `"`)
	}
	return ""
}

func shouldClearFlowOnComplete(err error) bool {
	var flowErr *OAuthFlowError
	if !errors.As(err, &flowErr) {
		return false
	}
	switch flowErr.Code {
	case OAuthErrorStateInvalid, OAuthErrorFlowInvalid, OAuthErrorProviderDisabled,
		OAuthErrorRegisterDisabled, OAuthErrorEmailConflict, OAuthErrorEmailRequired,
		OAuthErrorAlreadyRegistered, OAuthErrorRiskBlocked, OAuthErrorBanned,
		OAuthErrorAccountUnavailable, OAuthErrorIdentityConflict:
		return true
	default:
		return false
	}
}

func oauthAPIError(err error) error {
	var flowErr *OAuthFlowError
	if errors.As(err, &flowErr) {
		status := consts.StatusBadRequest
		switch flowErr.Code {
		case OAuthErrorBanned, OAuthErrorAccountUnavailable, OAuthErrorRiskBlocked:
			status = consts.StatusForbidden
		case OAuthErrorRegisterLimited:
			status = consts.StatusTooManyRequests
		}
		return apperrors.New(status, flowErr.Message)
	}
	return err
}
