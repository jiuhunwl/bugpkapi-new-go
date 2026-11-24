package auth

import (
	"context"
	"net/url"
	"strings"

	"bugpk-api/internal/config"
	apperrors "bugpk-api/internal/pkg/errors"
	"bugpk-api/internal/pkg/jsonrequest"
	"bugpk-api/internal/pkg/response"

	hertzapp "github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"github.com/cloudwego/hertz/pkg/route"
)

type Handler struct {
	cfg     config.Config
	service *Service
}

const (
	maxAuthJSONNestingDepth = 16
	maxAuthRequestBodyBytes = 64 * 1024
)

func NewHandler(cfg config.Config, service *Service) *Handler {
	return &Handler{cfg: cfg, service: service}
}

func (h *Handler) Register(group *route.RouterGroup) {
	group.GET("/captcha", h.captcha)
	group.POST("/captcha/:captchaId/verify", h.verifyCaptcha)
	group.POST("/login", h.login)
	group.POST("/2fa/login", h.twoFALogin)
	group.POST("/register", h.register)
	group.POST("/register/email-code", h.sendRegisterEmailCode)
	group.POST("/register/mobile-code", h.sendRegisterMobileCode)
	group.GET("/register/config", h.registerConfig)
	group.POST("/password-reset/account", h.checkPasswordResetAccount)
	group.POST("/password-reset/email-code", h.sendPasswordResetEmailCode)
	group.POST("/password-reset/verify", h.verifyPasswordResetCode)
	group.POST("/password-reset", h.resetPassword)
	group.POST("/refresh", h.refresh)
	group.POST("/logout", h.logout)
	group.GET("/codes", h.codes)
	// 2FA 管理：需要调用方自带 Bearer access token（与 /codes 同一鉴权模式）。
	group.GET("/2fa/status", h.twoFAStatus)
	group.POST("/2fa/setup", h.twoFASetup)
	group.POST("/2fa/enable", h.twoFAEnable)
	group.POST("/2fa/disable", h.twoFADisable)
	group.POST("/2fa/recovery-codes", h.twoFARegenerateRecoveryCodes)
}

func (h *Handler) captcha(ctx context.Context, c *hertzapp.RequestContext) {
	// 签发前先下发/刷新签名设备 cookie：注册评分需要「同一浏览器开了几个号」，
	// 而 HttpOnly 能避免它被页面脚本或第三方直接读取。
	h.ensureDeviceCookie(c)
	result, err := h.service.CaptchaWithIP(ctx, string(c.Query("scene")), binding(c), c.ClientIP())
	if err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, result)
}

func (h *Handler) verifyCaptcha(ctx context.Context, c *hertzapp.RequestContext) {
	var req CaptchaVerifyRequest
	if err := bindJSON(c, &req); err != nil {
		response.FromError(c, err)
		return
	}
	if err := h.service.VerifyCaptcha(ctx, c.Param("captchaId"), req, binding(c), c.ClientIP()); err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, true)
}

func (h *Handler) login(ctx context.Context, c *hertzapp.RequestContext) {
	var req LoginRequest
	if err := bindJSON(c, &req); err != nil {
		response.FromError(c, err)
		return
	}
	outcome, err := h.service.LoginWithOutcome(ctx, req, binding(c), c.ClientIP(), header(c, "User-Agent"))
	if err != nil {
		response.FromError(c, err)
		return
	}
	// 2FA 分支：密码正确但需要第二因子。不签发 refresh cookie，
	// 前端凭 challengeToken 走 /auth/2fa/login。
	if outcome.TwoFARequired {
		response.OK(c, LoginResult{
			TwoFARequired:  true,
			ChallengeToken: outcome.TwoFA.ChallengeToken,
			ExpiresIn:      outcome.TwoFA.ExpiresIn,
		})
		return
	}
	h.setRefreshCookie(c, outcome.Pair.RefreshToken, int(h.service.RefreshTTL().Seconds()))
	response.OK(c, LoginResult{AccessToken: outcome.Pair.AccessToken})
}

// twoFALogin 完成登录的第二步：challenge token + TOTP/恢复码换正式令牌。
func (h *Handler) twoFALogin(ctx context.Context, c *hertzapp.RequestContext) {
	var req TwoFALoginRequest
	if err := bindJSON(c, &req); err != nil {
		response.FromError(c, err)
		return
	}
	outcome, err := h.service.LoginTwoFA(ctx, req, c.ClientIP(), header(c, "User-Agent"))
	if err != nil {
		response.FromError(c, err)
		return
	}
	h.setRefreshCookie(c, outcome.Pair.RefreshToken, int(h.service.RefreshTTL().Seconds()))
	response.OK(c, LoginResult{AccessToken: outcome.Pair.AccessToken})
}

func (h *Handler) twoFAStatus(ctx context.Context, c *hertzapp.RequestContext) {
	result, err := h.service.TwoFAStatus(ctx, bearerToken(c))
	if err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, result)
}

func (h *Handler) twoFASetup(ctx context.Context, c *hertzapp.RequestContext) {
	result, err := h.service.TwoFASetup(ctx, bearerToken(c))
	if err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, result)
}

func (h *Handler) twoFAEnable(ctx context.Context, c *hertzapp.RequestContext) {
	var req TwoFAEnableRequest
	if err := bindJSON(c, &req); err != nil {
		response.FromError(c, err)
		return
	}
	result, err := h.service.TwoFAEnable(ctx, bearerToken(c), req)
	if err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, result)
}

func (h *Handler) twoFADisable(ctx context.Context, c *hertzapp.RequestContext) {
	var req TwoFADisableRequest
	if err := bindJSON(c, &req); err != nil {
		response.FromError(c, err)
		return
	}
	if err := h.service.TwoFADisable(ctx, bearerToken(c), req); err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, true)
}

func (h *Handler) twoFARegenerateRecoveryCodes(ctx context.Context, c *hertzapp.RequestContext) {
	var req TwoFARecoveryRegenerateRequest
	if err := bindJSON(c, &req); err != nil {
		response.FromError(c, err)
		return
	}
	result, err := h.service.TwoFARegenerateRecoveryCodes(ctx, bearerToken(c), req)
	if err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, result)
}

func (h *Handler) register(ctx context.Context, c *hertzapp.RequestContext) {
	var req RegisterRequest
	if err := bindJSON(c, &req); err != nil {
		response.FromError(c, err)
		return
	}
	risk := RegisterRiskContext{
		DeviceID:       h.deviceID(c),
		UserAgent:      header(c, "User-Agent"),
		AcceptLanguage: header(c, "Accept-Language"),
	}
	tokens, err := h.service.RegisterWithRisk(ctx, req, binding(c), c.ClientIP(), header(c, "User-Agent"), risk)
	if err != nil {
		response.FromError(c, err)
		return
	}
	h.setRefreshCookie(c, tokens.RefreshToken, int(h.service.RefreshTTL().Seconds()))
	response.OK(c, LoginResult{AccessToken: tokens.AccessToken})
}

func (h *Handler) registerConfig(ctx context.Context, c *hertzapp.RequestContext) {
	h.ensureDeviceCookie(c)
	cfg, err := h.service.RegisterConfig(ctx)
	if err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, cfg)
}

func (h *Handler) sendRegisterEmailCode(ctx context.Context, c *hertzapp.RequestContext) {
	var req SendRegisterEmailCodeRequest
	if err := bindJSON(c, &req); err != nil {
		response.FromError(c, err)
		return
	}
	lang := ResolveRequestEmailLang(header(c, "X-App-Locale"), header(c, "Accept-Language"))
	if err := h.service.SendRegisterEmailCode(ctx, req, binding(c), c.ClientIP(), lang); err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, true)
}

func (h *Handler) sendRegisterMobileCode(ctx context.Context, c *hertzapp.RequestContext) {
	var req SendRegisterMobileCodeRequest
	if err := bindJSON(c, &req); err != nil {
		response.FromError(c, err)
		return
	}
	if err := h.service.SendRegisterMobileCode(ctx, req, binding(c), c.ClientIP()); err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, true)
}

func (h *Handler) checkPasswordResetAccount(ctx context.Context, c *hertzapp.RequestContext) {
	var req PasswordResetAccountRequest
	if err := bindJSON(c, &req); err != nil {
		response.FromError(c, err)
		return
	}
	result, err := h.service.CheckPasswordResetAccount(ctx, req, c.ClientIP())
	if err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, result)
}

func (h *Handler) sendPasswordResetEmailCode(ctx context.Context, c *hertzapp.RequestContext) {
	var req PasswordResetEmailCodeRequest
	if err := bindJSON(c, &req); err != nil {
		response.FromError(c, err)
		return
	}
	lang := ResolveRequestEmailLang(header(c, "X-App-Locale"), header(c, "Accept-Language"))
	if err := h.service.SendPasswordResetEmailCode(ctx, req, binding(c), c.ClientIP(), lang); err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, true)
}

func (h *Handler) verifyPasswordResetCode(ctx context.Context, c *hertzapp.RequestContext) {
	var req PasswordResetVerifyRequest
	if err := bindJSON(c, &req); err != nil {
		response.FromError(c, err)
		return
	}
	result, err := h.service.VerifyPasswordResetCode(ctx, req)
	if err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, result)
}

func (h *Handler) resetPassword(ctx context.Context, c *hertzapp.RequestContext) {
	var req PasswordResetRequest
	if err := bindJSON(c, &req); err != nil {
		response.FromError(c, err)
		return
	}
	if err := h.service.ResetPassword(ctx, req); err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, true)
}

func (h *Handler) refresh(ctx context.Context, c *hertzapp.RequestContext) {
	if err := requireSameOriginCookieRequest(c); err != nil {
		response.FromError(c, err)
		return
	}
	var lastErr error
	for _, refreshToken := range refreshCookieValues(c, h.cfg.Auth.RefreshCookieName) {
		tokens, err := h.service.Refresh(ctx, refreshToken, c.ClientIP(), header(c, "User-Agent"))
		if err == nil {
			// 宽限窗口内的重复刷新只补发 access token；此时浏览器里的
			// refresh cookie 已由首次消费的响应轮换，不能再覆盖。
			if tokens.RefreshToken != "" {
				h.setRefreshCookie(c, tokens.RefreshToken, int(h.service.RefreshTTL().Seconds()))
			}
			c.String(consts.StatusOK, tokens.AccessToken)
			return
		}
		lastErr = err
		// A stale cookie is common while migrating the cookie path. Continue with
		// the next same-name cookie instead of returning 401 prematurely.
		if shouldClearRefreshCookie(err) {
			continue
		}
		break
	}
	if lastErr == nil {
		lastErr = apperrors.New(consts.StatusUnauthorized, "Unauthorized Exception")
	}
	if shouldClearRefreshCookie(lastErr) {
		h.clearRefreshCookie(c)
	}
	response.FromError(c, lastErr)
}

func shouldClearRefreshCookie(err error) bool {
	business, ok := apperrors.AsBusinessError(err)
	return ok && business.StatusCode == consts.StatusUnauthorized
}

func (h *Handler) logout(ctx context.Context, c *hertzapp.RequestContext) {
	if err := requireSameOriginCookieRequest(c); err != nil {
		response.FromError(c, err)
		return
	}
	err := h.service.Logout(ctx, string(c.Cookie(h.cfg.Auth.RefreshCookieName)), c.ClientIP(), header(c, "User-Agent"))
	// Always remove the browser credential, even when server-side revocation is
	// temporarily unavailable. The error still reports that revocation failed.
	h.clearRefreshCookie(c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, true)
}

func requireSameOriginCookieRequest(c *hertzapp.RequestContext) error {
	fetchSite := strings.ToLower(strings.TrimSpace(header(c, "Sec-Fetch-Site")))
	switch fetchSite {
	case "none", "same-origin":
		return nil
	case "same-site", "cross-site":
		return apperrors.New(consts.StatusForbidden, "跨站请求被拒绝")
	case "":
		// Older browsers and non-browser clients may omit Fetch Metadata.
		// Validate browser source headers when present, while preserving CLI and
		// service-to-service refresh/logout calls that carry neither header.
		requestHost := strings.ToLower(strings.TrimSpace(string(c.Request.Host())))
		for _, name := range []string{"Origin", "Referer"} {
			raw := strings.TrimSpace(header(c, name))
			if raw == "" {
				continue
			}
			parsed, err := url.Parse(raw)
			if err != nil || parsed.User != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || requestHost == "" || !strings.EqualFold(parsed.Host, requestHost) {
				return apperrors.New(consts.StatusForbidden, "跨站请求被拒绝")
			}
			return nil
		}
		return nil
	default:
		return apperrors.New(consts.StatusForbidden, "跨站请求被拒绝")
	}
}

func (h *Handler) codes(ctx context.Context, c *hertzapp.RequestContext) {
	codes, err := h.service.Codes(ctx, bearerToken(c))
	if err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, codes)
}

func (h *Handler) setRefreshCookie(c *hertzapp.RequestContext, value string, maxAge int) {
	h.setRefreshCookieAtPath(c, value, maxAge, h.refreshCookiePath())
	legacyPath := h.legacyRefreshCookiePath()
	if legacyPath != h.refreshCookiePath() {
		h.setRefreshCookieAtPath(c, "", -1, legacyPath)
	}
}

func (h *Handler) setRefreshCookieAtPath(c *hertzapp.RequestContext, value string, maxAge int, path string) {
	cookie := protocol.AcquireCookie()
	defer protocol.ReleaseCookie(cookie)
	cookie.SetKey(h.cfg.Auth.RefreshCookieName)
	cookie.SetValue(url.QueryEscape(value))
	cookie.SetMaxAge(maxAge)
	cookie.SetPath(path)
	cookie.SetSameSite(sameSite(h.cfg.Auth.RefreshCookieSameSite))
	cookie.SetSecure(h.cfg.Auth.RefreshCookieSecure)
	cookie.SetHTTPOnly(true)
	// ResponseHeader.SetCookie replaces cookies with the same name even when
	// their paths differ. Add preserves both the new narrow cookie and the
	// one-time deletion header for the legacy wider path.
	c.Response.Header.Add("Set-Cookie", string(cookie.Cookie()))
}

func (h *Handler) clearRefreshCookie(c *hertzapp.RequestContext) {
	h.setRefreshCookieAtPath(c, "", -1, h.refreshCookiePath())
	legacyPath := h.legacyRefreshCookiePath()
	if legacyPath != h.refreshCookiePath() {
		h.setRefreshCookieAtPath(c, "", -1, legacyPath)
	}
}

// deviceID reads the signed device cookie and returns its random id, or "" when
// the cookie is absent/forged. A missing cookie only adds a small risk score; it
// never blocks a human browser that has not visited the captcha endpoint.
func (h *Handler) deviceID(c *hertzapp.RequestContext) string {
	for _, value := range refreshCookieValues(c, DeviceCookieName) {
		if id := verifyDeviceID(h.cfg.Auth.Secret, value); id != "" {
			return id
		}
	}
	return ""
}

func (h *Handler) ensureDeviceCookie(c *hertzapp.RequestContext) string {
	if id := h.deviceID(c); id != "" {
		return id
	}
	value, id, err := newDeviceID(h.cfg.Auth.Secret)
	if err != nil {
		return ""
	}
	cookie := protocol.AcquireCookie()
	defer protocol.ReleaseCookie(cookie)
	cookie.SetKey(DeviceCookieName)
	cookie.SetValue(url.QueryEscape(value))
	cookie.SetMaxAge(deviceCookieMaxAgeSeconds)
	cookie.SetPath(h.legacyRefreshCookiePath())
	cookie.SetSameSite(sameSite(h.cfg.Auth.RefreshCookieSameSite))
	cookie.SetSecure(h.cfg.Auth.RefreshCookieSecure)
	cookie.SetHTTPOnly(true)
	c.Response.Header.Add("Set-Cookie", string(cookie.Cookie()))
	return id
}

func (h *Handler) refreshCookiePath() string {
	base := h.legacyRefreshCookiePath()
	if base == "/" {
		return "/auth"
	}
	return base + "/auth"
}

func (h *Handler) legacyRefreshCookiePath() string {
	path := strings.TrimRight(strings.TrimSpace(h.cfg.API.SystemPrefix), "/")
	if path == "" {
		return "/"
	}
	return path
}

func bindJSON(c *hertzapp.RequestContext, target any) error {
	if contentLength := c.Request.Header.ContentLength(); contentLength > maxAuthRequestBodyBytes {
		return apperrors.New(consts.StatusRequestEntityTooLarge, "认证请求体不能超过64 KiB")
	}
	body, err := c.Body()
	if err != nil {
		return apperrors.Wrap(consts.StatusBadRequest, "请求参数不合法", err)
	}
	if len(body) == 0 {
		return nil
	}
	if len(body) > maxAuthRequestBodyBytes {
		return apperrors.New(consts.StatusRequestEntityTooLarge, "认证请求体不能超过64 KiB")
	}
	if err := jsonrequest.UnmarshalUnambiguous(body, target, maxAuthJSONNestingDepth); err != nil {
		return apperrors.Wrap(consts.StatusBadRequest, "请求参数不合法", err)
	}
	return nil
}

func binding(c *hertzapp.RequestContext) string {
	return CaptchaBinding(header(c, "User-Agent"), header(c, "Accept-Language"))
}

func header(c *hertzapp.RequestContext, key string) string {
	return string(c.Request.Header.Peek(key))
}

// refreshCookieValues preserves all same-name cookies. Browsers can send both
// the current /auth cookie and a legacy wider-path cookie during migration;
// RequestContext.Cookie returns only one of them.
func refreshCookieValues(c *hertzapp.RequestContext, name string) []string {
	raw := header(c, "Cookie")
	values := make([]string, 0, 2)
	for _, part := range strings.Split(raw, ";") {
		key, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(key), name) {
			continue
		}
		value = strings.Trim(strings.TrimSpace(value), `"`)
		if decoded, err := url.QueryUnescape(value); err == nil {
			value = decoded
		}
		if value != "" {
			values = append(values, value)
		}
	}
	if len(values) == 0 {
		if value := string(c.Cookie(name)); value != "" {
			values = append(values, value)
		}
	}
	return values
}

func bearerToken(c *hertzapp.RequestContext) string {
	value := header(c, "Authorization")
	if strings.HasPrefix(strings.ToLower(value), "bearer ") {
		return strings.TrimSpace(value[7:])
	}
	return ""
}

func sameSite(value string) protocol.CookieSameSite {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "none":
		return protocol.CookieSameSiteNoneMode
	case "strict":
		return protocol.CookieSameSiteStrictMode
	case "lax", "":
		return protocol.CookieSameSiteLaxMode
	default:
		return protocol.CookieSameSiteLaxMode
	}
}
