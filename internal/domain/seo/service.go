package seo

import (
	"context"
	"database/sql"
)

// sitemapRepo 是 sitemap 生成所需的数据访问接口（便于测试注入 fake）。
type sitemapRepo interface {
	// ListedInterfaces 返回已上架接口的 id 与最后更新日期（YYYY-MM-DD）。
	ListedInterfaces(ctx context.Context) ([]struct {
		ID      int64
		Updated string
	}, error)
	// SiteURL 返回后台配置的站点对外基础地址（bp_site_config.site_url，id=1）；未配置返回空串。
	SiteURL(ctx context.Context) (string, error)
	// Noindex 返回后台收录开关（bp_site_config.noindex，id=1）；true 表示禁止收录。
	Noindex(ctx context.Context) (bool, error)
}

// Service 提供 SEO 相关业务能力（sitemap 等）。
type Service struct {
	repo sitemapRepo
}

func NewService(db *sql.DB) *Service {
	return &Service{repo: NewRepository(db)}
}

// Sitemap 生成 sitemap XML（baseURL 为空时返回错误，由 handler 决定回退）。
func (s *Service) Sitemap(ctx context.Context, baseURL string) ([]byte, error) {
	return s.BuildSitemap(ctx, baseURL)
}

// SiteURL 返回后台配置的站点对外基础地址；未配置时返回空串。
func (s *Service) SiteURL(ctx context.Context) (string, error) {
	return s.repo.SiteURL(ctx)
}

// Noindex 返回后台收录开关状态；true 表示站点禁止收录（sitemap 应停发）。
func (s *Service) Noindex(ctx context.Context) (bool, error) {
	return s.repo.Noindex(ctx)
}
