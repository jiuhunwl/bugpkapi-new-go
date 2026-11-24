package systemsettings

import (
	"context"
	"strings"

	"bugpk-api/internal/domain/auth"
	"bugpk-api/internal/domain/rbac"
	"bugpk-api/internal/pkg/imagehost"
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

func (h *Handler) RegisterSystem(group *route.RouterGroup) {
	group.GET("/register/open-config", h.openConfig)
	group.PUT("/register/open-config", h.saveOpenConfig)
	group.GET("/register/email-domains", h.emailDomainRules)
	group.PUT("/register/email-domains", h.saveEmailDomainRules)
	group.GET("/register/email-config", h.emailConfig)
	group.PUT("/register/email-config", h.saveEmailConfig)
	group.POST("/register/email-config/test", h.testEmailConfig)
	group.GET("/register/mobile-config", h.mobileConfig)
	group.PUT("/register/mobile-config", h.saveMobileConfig)
	group.POST("/register/mobile-config/test", h.testMobileConfig)
	group.GET("/login/qq-config", h.qqLoginConfig)
	group.PUT("/login/qq-config", h.saveQQLoginConfig)
	group.GET("/session-config", h.sessionConfig)
	group.PUT("/session-config", h.saveSessionConfig)
	group.GET("/captcha-config", h.captchaConfig)
	group.PUT("/captcha-config", h.saveCaptchaConfig)
	group.PUT("/captcha-config/global", h.saveCaptchaGlobal)
	group.PUT("/captcha-config/scenes", h.saveCaptchaScenes)
	group.GET("/custom-code", h.customCode)
	group.PUT("/custom-code", h.saveCustomCode)
	group.GET("/image-host-config", h.imageHostConfig)
	group.PUT("/image-host-config", h.saveImageHostConfig)
	group.POST("/image-host-config/test", h.testImageHostConfig)
}

func (h *Handler) RegisterPublicSite(group *route.RouterGroup) {
	group.GET("/custom-code", h.publicCustomCode)
}

func (h *Handler) publicCustomCode(ctx context.Context, c *hertzapp.RequestContext) {
	result, err := h.service.PublicCustomCode(ctx)
	respond(c, result, err)
}

func (h *Handler) customCode(ctx context.Context, c *hertzapp.RequestContext) {
	principal, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	result, err := h.service.AdminCustomCode(ctx, principal)
	respond(c, result, err)
}

func (h *Handler) saveCustomCode(ctx context.Context, c *hertzapp.RequestContext) {
	principal, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	var req CustomCodeConfig
	if !bind(c, &req) {
		return
	}
	respond(c, true, h.service.SaveCustomCode(ctx, principal, req))
}

func (h *Handler) imageHostConfig(ctx context.Context, c *hertzapp.RequestContext) {
	principal, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	result, err := h.service.ImageHostConfig(ctx, principal)
	respond(c, result, err)
}

func (h *Handler) saveImageHostConfig(ctx context.Context, c *hertzapp.RequestContext) {
	principal, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	var req imagehost.Config
	if !bind(c, &req) {
		return
	}
	respond(c, true, h.service.SaveImageHostConfig(ctx, principal, req))
}

func (h *Handler) testImageHostConfig(ctx context.Context, c *hertzapp.RequestContext) {
	principal, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	var req imagehost.Config
	if !bind(c, &req) {
		return
	}
	result, err := h.service.TestImageHostConfig(ctx, principal, req)
	respond(c, result, err)
}

func (h *Handler) openConfig(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	result, err := h.service.RegisterOpenConfig(ctx, p)
	respond(c, result, err)
}

func (h *Handler) saveOpenConfig(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	var req RegisterOpenConfig
	if !bind(c, &req) {
		return
	}
	respond(c, true, h.service.SaveRegisterOpenConfig(ctx, p, req))
}

func (h *Handler) emailDomainRules(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	result, err := h.service.EmailDomainRules(ctx, p)
	respond(c, result, err)
}

func (h *Handler) saveEmailDomainRules(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	var req EmailDomainRules
	if !bind(c, &req) {
		return
	}
	respond(c, true, h.service.SaveEmailDomainRules(ctx, p, req))
}

func (h *Handler) emailConfig(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	result, err := h.service.EmailConfig(ctx, p)
	respond(c, result, err)
}

func (h *Handler) saveEmailConfig(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	var req EmailConfig
	if !bind(c, &req) {
		return
	}
	respond(c, true, h.service.SaveEmailConfig(ctx, p, req))
}

func (h *Handler) testEmailConfig(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	var req EmailConfig
	if !bind(c, &req) {
		return
	}
	req.Language = auth.ResolveEmailLanguage(
		req.Language,
		"",
		string(c.Request.Header.Peek("X-App-Locale")),
		string(c.Request.Header.Peek("Accept-Language")),
		"",
	)
	respond(c, true, h.service.TestEmailConfig(ctx, p, req))
}

func (h *Handler) mobileConfig(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	result, err := h.service.MobileConfig(ctx, p)
	respond(c, result, err)
}

func (h *Handler) saveMobileConfig(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	var req MobileConfig
	if !bind(c, &req) {
		return
	}
	respond(c, true, h.service.SaveMobileConfig(ctx, p, req))
}

func (h *Handler) testMobileConfig(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	var req MobileConfig
	if !bind(c, &req) {
		return
	}
	respond(c, true, h.service.TestMobileConfig(ctx, p, req))
}

func (h *Handler) qqLoginConfig(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	result, err := h.service.QQLoginConfig(ctx, p)
	respond(c, result, err)
}

func (h *Handler) saveQQLoginConfig(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	var req QQLoginConfig
	if !bind(c, &req) {
		return
	}
	respond(c, true, h.service.SaveQQLoginConfig(ctx, p, req))
}

func (h *Handler) captchaConfig(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	result, err := h.service.CaptchaConfig(ctx, p)
	respond(c, result, err)
}

func (h *Handler) saveCaptchaConfig(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	var req SaveCaptchaConfigRequest
	if !bind(c, &req) {
		return
	}
	respond(c, true, h.service.SaveCaptchaConfig(ctx, p, req))
}

func (h *Handler) saveCaptchaGlobal(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	var req SaveCaptchaGlobalRequest
	if !bind(c, &req) {
		return
	}
	respond(c, true, h.service.SaveCaptchaGlobal(ctx, p, req))
}

func (h *Handler) saveCaptchaScenes(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	var req SaveCaptchaScenesRequest
	if !bind(c, &req) {
		return
	}
	respond(c, true, h.service.SaveCaptchaScenes(ctx, p, req))
}

func (h *Handler) principal(ctx context.Context, c *hertzapp.RequestContext) (Principal, bool) {
	p, err := h.service.Authenticate(ctx, bearer(c))
	if err != nil {
		response.FromError(c, err)
		return Principal{}, false
	}
	return p, true
}

type Principal = rbac.Principal

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

func (h *Handler) sessionConfig(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	result, err := h.service.SessionConfig(ctx, p)
	respond(c, result, err)
}

func (h *Handler) saveSessionConfig(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	var req SessionConfig
	if !bind(c, &req) {
		return
	}
	result, err := h.service.SaveSessionConfig(ctx, p, req)
	respond(c, result, err)
}
