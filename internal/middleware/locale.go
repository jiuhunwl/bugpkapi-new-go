package middleware

import (
	"context"

	"bugpk-api/internal/pkg/i18n"

	hertzapp "github.com/cloudwego/hertz/pkg/app"
)

// Locale 解析请求语言并写入上下文。
// 只挂载在 API 分组，静态资源不需要语言协商。
func Locale(resolver i18n.LocaleResolver) hertzapp.HandlerFunc {
	if resolver == nil {
		return func(ctx context.Context, c *hertzapp.RequestContext) {
			c.Next(ctx)
		}
	}
	return func(ctx context.Context, c *hertzapp.RequestContext) {
		resolved := resolver.Resolve(
			string(c.Query("lang")),
			string(c.Request.Header.Peek("X-App-Locale")),
			string(c.Request.Header.Peek("Accept-Language")),
		)
		ctx = i18n.WithLocale(ctx, resolved)
		c.Response.Header.Set("Content-Language", resolved)
		c.Response.Header.Set("Vary", "X-App-Locale, Accept-Language")
		c.Next(ctx)
	}
}
