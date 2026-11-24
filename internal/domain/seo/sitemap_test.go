package seo

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/cloudwego/hertz/pkg/app"
	hertzconfig "github.com/cloudwego/hertz/pkg/common/config"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"github.com/cloudwego/hertz/pkg/route"

	"bugpk-api/internal/config"
)

// fakeRepo 用内存数据模拟接口列表与 SEO 配置，避免依赖真实数据库。
type fakeRepo struct {
	items []struct {
		ID      int64
		Updated string
	}
	siteURL string
	noindex bool
}

func (f *fakeRepo) ListedInterfaces(ctx context.Context) ([]struct {
	ID      int64
	Updated string
}, error) {
	return f.items, nil
}

func (f *fakeRepo) SiteURL(ctx context.Context) (string, error) {
	return f.siteURL, nil
}

func (f *fakeRepo) Noindex(ctx context.Context) (bool, error) {
	return f.noindex, nil
}

func TestBuildSitemap_StaticPagesAndInterfaces(t *testing.T) {
	svc := &Service{repo: &fakeRepo{
		items: []struct {
			ID      int64
			Updated string
		}{
			{ID: 1, Updated: "2026-08-01"},
			{ID: 7, Updated: "2026-08-05"},
		},
	}}
	body, err := svc.BuildSitemap(context.Background(), "https://api.example.com")
	if err != nil {
		t.Fatalf("BuildSitemap err = %v", err)
	}
	text := string(body)

	// XML 头与根元素
	if !strings.Contains(text, `<?xml version="1.0"`) {
		t.Errorf("missing xml header")
	}
	if !strings.Contains(text, `<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">`) {
		t.Errorf("missing urlset root")
	}
	// 静态公开页面
	for _, want := range []string{
		"https://api.example.com/",
		"https://api.example.com/apilist",
		"https://api.example.com/apis",
		"https://api.example.com/links",
		"https://api.example.com/donate",
		"https://api.example.com/privacy-policy",
		"https://api.example.com/terms-of-service",
	} {
		if !strings.Contains(text, "<loc>"+want+"</loc>") {
			t.Errorf("missing loc %s", want)
		}
	}
	// 数据库接口详情页
	for _, want := range []string{
		"https://api.example.com/docs/1",
		"https://api.example.com/docs/7",
	} {
		if !strings.Contains(text, "<loc>"+want+"</loc>") {
			t.Errorf("missing interface loc %s", want)
		}
	}
	// lastmod 存在
	if !strings.Contains(text, "<lastmod>2026-08-05</lastmod>") {
		t.Errorf("missing lastmod for interface")
	}
}

func TestBuildSitemap_EmptyBaseURLError(t *testing.T) {
	svc := &Service{repo: &fakeRepo{}}
	_, err := svc.BuildSitemap(context.Background(), "   ")
	if err == nil {
		t.Fatalf("expected error for empty base url")
	}
}

func TestSitemapXMLWellFormed(t *testing.T) {
	svc := &Service{repo: &fakeRepo{
		items: []struct {
			ID      int64
			Updated string
		}{
			{ID: 2, Updated: "2026-07-30"},
		},
	}}
	body, err := svc.BuildSitemap(context.Background(), "https://example.com")
	if err != nil {
		t.Fatalf("BuildSitemap err = %v", err)
	}
	text := strings.TrimSpace(string(body))
	if !strings.HasPrefix(text, `<?xml`) {
		t.Errorf("sitemap should start with xml header")
	}
	if !strings.HasSuffix(text, `</urlset>`) {
		t.Errorf("sitemap should end with </urlset>")
	}
	// 每个 <url> 必须同时有 <loc>
	re := regexp.MustCompile(`<url>`)
	locs := regexp.MustCompile(`<loc>`)
	if re.FindAllString(text, -1) == nil {
		t.Errorf("no <url> entries")
	}
	if len(re.FindAllString(text, -1)) != len(locs.FindAllString(text, -1)) {
		t.Errorf("url/loc count mismatch")
	}
}

// TestResolveSitemapBaseURL 覆盖域名优先级：DB site_url > config.SEO.SiteURL > 请求 Host。
func TestResolveSitemapBaseURL(t *testing.T) {
	cases := []struct {
		name string
		db   string
		cfg  string
		host string
		want string
	}{
		{name: "db first", db: "https://db.example.com/", cfg: "https://cfg.example.com", host: "host.example.com", want: "https://db.example.com"},
		{name: "config fallback", db: "", cfg: "https://cfg.example.com", host: "host.example.com", want: "https://cfg.example.com"},
		{name: "host fallback", db: "", cfg: "", host: "host.example.com", want: "https://host.example.com"},
		{name: "db trims trailing slash", db: "https://db.example.com", cfg: "https://cfg.example.com", host: "", want: "https://db.example.com"},
		{name: "all empty", db: "", cfg: "", host: "", want: ""},
		{name: "whitespace ignored", db: "  ", cfg: "  ", host: "  ", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveSitemapBaseURL(tc.db, tc.cfg, tc.host); got != tc.want {
				t.Errorf("resolveSitemapBaseURL(%q,%q,%q) = %q, want %q", tc.db, tc.cfg, tc.host, got, tc.want)
			}
		})
	}
}

// TestServiceSiteURLAndNoindex 覆盖 Service 对 DB site_url / noindex 的透传读取。
func TestServiceSiteURLAndNoindex(t *testing.T) {
	svc := &Service{repo: &fakeRepo{siteURL: "https://db.example.com", noindex: true}}
	if got, err := svc.SiteURL(context.Background()); err != nil || got != "https://db.example.com" {
		t.Errorf("SiteURL = %q, err=%v; want https://db.example.com", got, err)
	}
	if got, err := svc.Noindex(context.Background()); err != nil || !got {
		t.Errorf("Noindex = %v, err=%v; want true", got, err)
	}
}

// TestSitemapHandler_NoindexReturns404 覆盖 noindex=1 时 sitemap 返回 404。
func TestSitemapHandler_NoindexReturns404(t *testing.T) {
	svc := &Service{repo: &fakeRepo{noindex: true}}
	h := NewHandler(svc, config.Config{})
	engine := route.NewEngine(hertzconfig.NewOptions([]hertzconfig.Option{}))
	engine.GET("/system-api/seo/sitemap.xml", func(ctx context.Context, c *app.RequestContext) {
		h.sitemap(ctx, c)
	})

	resp := ut.PerformRequest(engine, consts.MethodGet, "/system-api/seo/sitemap.xml", nil)
	if resp.Code != consts.StatusNotFound {
		t.Fatalf("status = %d, want %d; body=%s", resp.Code, consts.StatusNotFound, resp.Body.String())
	}
}

// TestSitemapHandler_DBSiteURLPriority 覆盖 sitemap 域名优先使用 DB site_url。
func TestSitemapHandler_DBSiteURLPriority(t *testing.T) {
	svc := &Service{repo: &fakeRepo{siteURL: "https://db.example.com"}}
	h := NewHandler(svc, config.Config{SEO: config.SEOConfig{SiteURL: "https://cfg.example.com"}})
	engine := route.NewEngine(hertzconfig.NewOptions([]hertzconfig.Option{}))
	engine.GET("/system-api/seo/sitemap.xml", func(ctx context.Context, c *app.RequestContext) {
		h.sitemap(ctx, c)
	})

	resp := ut.PerformRequest(engine, consts.MethodGet, "/system-api/seo/sitemap.xml", nil)
	if resp.Code != consts.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", resp.Code, consts.StatusOK, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), "<loc>https://db.example.com/</loc>") {
		t.Errorf("sitemap should use DB site_url, body=%s", resp.Body.String())
	}
	if strings.Contains(resp.Body.String(), "https://cfg.example.com") {
		t.Errorf("sitemap should not fall back to config when DB site_url set, body=%s", resp.Body.String())
	}
}

// TestSitemapHandler_ConfigFallback 覆盖 DB site_url 为空时回退 config.SEO.SiteURL。
func TestSitemapHandler_ConfigFallback(t *testing.T) {
	svc := &Service{repo: &fakeRepo{}}
	h := NewHandler(svc, config.Config{SEO: config.SEOConfig{SiteURL: "https://cfg.example.com"}})
	engine := route.NewEngine(hertzconfig.NewOptions([]hertzconfig.Option{}))
	engine.GET("/system-api/seo/sitemap.xml", func(ctx context.Context, c *app.RequestContext) {
		h.sitemap(ctx, c)
	})

	resp := ut.PerformRequest(engine, consts.MethodGet, "/system-api/seo/sitemap.xml", nil)
	if resp.Code != consts.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", resp.Code, consts.StatusOK, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), "<loc>https://cfg.example.com/</loc>") {
		t.Errorf("sitemap should fall back to config site url, body=%s", resp.Body.String())
	}
}
