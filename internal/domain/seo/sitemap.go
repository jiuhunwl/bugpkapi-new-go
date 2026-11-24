package seo

import (
	"context"
	"database/sql"
	"encoding/xml"
	"fmt"
	"sort"
	"strings"

	"bugpk-api/internal/config"
)

// ============================================================
// SEO 支持：动态 sitemap.xml
// 公开接口 /system-api/seo/sitemap.xml（无需登录），供搜索引擎抓取。
// 内容：
//   1. 固定公开页面（首页、接口市场、接口列表、友链、捐赠、隐私政策与用户协议）
//   2. 数据库 bp_interface_api 中已上架（status=1）的接口详情页 /docs/<id>
// 域名取自环境变量 VITE_APP_SITE_URL（.env 配置）；未配置时回退请求 Host。
// ============================================================

// urlSet 对应 sitemap XML 的 <urlset> 根元素。
type urlSet struct {
	XMLName xml.Name   `xml:"urlset"`
	Xmlns   string     `xml:"xmlns,attr"`
	URLs    []urlEntry `xml:"url"`
}

type urlEntry struct {
	Loc        string `xml:"loc"`
	Lastmod    string `xml:"lastmod,omitempty"`
	Changefreq string `xml:"changefreq,omitempty"`
	Priority   string `xml:"priority,omitempty"`
}

// Repository 提供 sitemap 所需的数据查询。
type Repository struct {
	db *sql.DB
}

func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db}
}

// ListedInterfaces 返回已上架接口的 id 与更新时间（用于接口详情页 URL）。
func (r *Repository) ListedInterfaces(ctx context.Context) ([]struct {
	ID      int64
	Updated string
}, error) {
	rows, err := r.db.QueryContext(ctx, `
		select id, date_format(updated_at, '%Y-%m-%d') from bp_interface_api where status = 1 order by id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []struct {
		ID      int64
		Updated string
	}
	for rows.Next() {
		var it struct {
			ID      int64
			Updated string
		}
		if err := rows.Scan(&it.ID, &it.Updated); err != nil {
			return nil, err
		}
		items = append(items, it)
	}
	return items, rows.Err()
}

// SiteURL 查询后台配置的站点对外基础地址（bp_site_config.site_url，id=1）。
// 未配置（空串或无行）时返回空串，由 handler 依次回退 config.SEO.SiteURL 与请求 Host。
func (r *Repository) SiteURL(ctx context.Context) (string, error) {
	var value string
	err := r.db.QueryRowContext(ctx, "select site_url from bp_site_config where id=1").Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(value), nil
}

// Noindex 查询后台收录开关（bp_site_config.noindex，id=1）。
// 无行时默认不开启收录限制（返回 false）。
func (r *Repository) Noindex(ctx context.Context) (bool, error) {
	var value int
	err := r.db.QueryRowContext(ctx, "select noindex from bp_site_config where id=1").Scan(&value)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return value == 1, nil
}

// BuildSitemap 生成 sitemap XML 文本。
// baseURL: 站点对外基础地址（如 https://api.example.com，不含结尾斜杠）
func (s *Service) BuildSitemap(ctx context.Context, baseURL string) ([]byte, error) {
	base := strings.TrimSpace(baseURL)
	base = strings.TrimRight(base, "/")
	if base == "" {
		return nil, fmt.Errorf("seo: site base url is empty")
	}

	// 1) 固定公开页面
	entries := []urlEntry{
		{Loc: base + "/", Changefreq: "daily", Priority: "1.0"},
		{Loc: base + "/apilist", Changefreq: "daily", Priority: "0.9"},
		{Loc: base + "/apis", Changefreq: "daily", Priority: "0.9"},
		{Loc: base + "/links", Changefreq: "weekly", Priority: "0.5"},
		{Loc: base + "/donate", Changefreq: "monthly", Priority: "0.3"},
		{Loc: base + "/privacy-policy", Changefreq: "monthly", Priority: "0.3"},
		{Loc: base + "/terms-of-service", Changefreq: "monthly", Priority: "0.3"},
	}

	// 2) 数据库已上架接口详情页 /docs/<id>
	interfaces, err := s.repo.ListedInterfaces(ctx)
	if err != nil {
		return nil, err
	}
	for _, it := range interfaces {
		entries = append(entries, urlEntry{
			Loc:        fmt.Sprintf("%s/docs/%d", base, it.ID),
			Lastmod:    it.Updated,
			Changefreq: "weekly",
			Priority:   "0.7",
		})
	}

	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].Loc < entries[j].Loc
	})

	set := urlSet{Xmlns: "http://www.sitemaps.org/schemas/sitemap/0.9", URLs: entries}
	out, err := xml.MarshalIndent(set, "", "  ")
	if err != nil {
		return nil, err
	}
	return []byte(xml.Header + string(out) + "\n"), nil
}

// ConfigHelper 解析配置里的 SEO 域名。
func ConfigHelper(cfg config.Config) string {
	return strings.TrimSpace(cfg.SEO.SiteURL)
}
