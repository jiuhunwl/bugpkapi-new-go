package seo

import (
	"context"
	"strings"

	"bugpk-api/internal/config"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"github.com/cloudwego/hertz/pkg/route"
)

// Handler 提供公开 SEO 接口（无需登录）。
type Handler struct {
	service *Service
	cfg     config.Config
}

func NewHandler(service *Service, cfg config.Config) *Handler {
	return &Handler{service: service, cfg: cfg}
}

// Register 注册公开路由：
//
//	GET /system-api/seo/sitemap.xml → 动态 sitemap
func (h *Handler) Register(g *route.RouterGroup) {
	g.GET("/sitemap.xml", h.sitemap)
}

// sitemap 返回 XML 格式的 sitemap；域名优先级：DB bp_site_config.site_url > config.SEO.SiteURL > 请求 Host。
// 收录开关 noindex=1 时返回 404，避免继续引导爬虫抓取（SEO-08）。
func (h *Handler) sitemap(ctx context.Context, c *app.RequestContext) {
	noindex, err := h.service.Noindex(ctx)
	if err != nil {
		c.JSON(consts.StatusInternalServerError, map[string]string{"error": "sitemap generation failed"})
		return
	}
	if noindex {
		c.AbortWithStatus(consts.StatusNotFound)
		return
	}

	dbSiteURL, err := h.service.SiteURL(ctx)
	if err != nil {
		c.JSON(consts.StatusInternalServerError, map[string]string{"error": "sitemap generation failed"})
		return
	}
	baseURL := resolveSitemapBaseURL(dbSiteURL, ConfigHelper(h.cfg), strings.TrimSpace(string(c.Host())))
	if baseURL == "" {
		c.JSON(consts.StatusInternalServerError, map[string]string{"error": "site url not configured"})
		return
	}

	body, err := h.service.Sitemap(ctx, baseURL)
	if err != nil {
		c.JSON(consts.StatusInternalServerError, map[string]string{"error": "sitemap generation failed"})
		return
	}
	c.Data(consts.StatusOK, "application/xml; charset=utf-8", body)
}

// resolveSitemapBaseURL 按「DB site_url > config.SEO.SiteURL > 请求 Host」的优先级解析 sitemap 域名。
// 前两者均去掉结尾斜杠；Host 兜底时补 https:// 前缀。全部为空时返回空串。
func resolveSitemapBaseURL(dbSiteURL, configSiteURL, requestHost string) string {
	if value := strings.TrimSpace(dbSiteURL); value != "" {
		return strings.TrimRight(value, "/")
	}
	if value := strings.TrimSpace(configSiteURL); value != "" {
		return strings.TrimRight(value, "/")
	}
	if host := strings.TrimSpace(requestHost); host != "" {
		return "https://" + host
	}
	return ""
}
