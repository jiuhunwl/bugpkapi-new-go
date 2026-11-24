package app

import (
	"context"
	"strings"

	"bugpk-api/internal/config"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

// corsAllowedMethods 预检请求允许的 HTTP 方法。
const corsAllowedMethods = "GET, POST, PUT, PATCH, DELETE, OPTIONS"

// corsAllowedHeaders 预检请求允许的请求头（覆盖前端常用的 Authorization / Content-Type 等）。
const corsAllowedHeaders = "Authorization, Content-Type, X-Requested-With, X-CSRF-Token, Accept, Accept-Language, X-App-Locale, Origin"

const corsExposedHeaders = "X-Request-ID, X-Bugpk-Account-Status, X-Bugpk-Ban-Type, X-Bugpk-Ban-Until"

// corsMaxAge 预检结果缓存时间（秒）。
const corsMaxAge = "86400"

// corsMiddleware 返回处理跨域请求的 Hertz 中间件。
//
// 行为约定：
//   - AllowedOrigins 为空（仅同域部署）时返回 nil，调用方不注册该中间件，对正常请求零影响；
//   - AllowedOrigins 含 "*" 时对所有来源放行（Access-Control-Allow-Origin: *，不携带凭据）；
//   - 其余情况仅允许列表内的来源：实际请求回显 Origin + Allow-Credentials，预检请求返回 204；
//   - 未匹配来源的预检请求返回 403；未匹配来源的实际请求不附加跨域头（浏览器会拦截响应）。
func corsMiddleware(cfg config.CORSConfig) app.HandlerFunc {
	allowed := normalizeCORSOrigins(cfg.AllowedOrigins)
	if len(allowed) == 0 {
		return nil
	}
	allowAll := containsCORSOrigin(allowed, "*")

	return func(ctx context.Context, c *app.RequestContext) {
		origin := strings.TrimSpace(string(c.GetHeader("Origin")))
		isPreflight := string(c.Method()) == consts.MethodOptions &&
			len(c.GetHeader("Access-Control-Request-Method")) > 0

		allowedOrigin := ""
		allowCredentials := false
		if allowAll {
			allowedOrigin = "*"
		} else if origin != "" && containsCORSOrigin(allowed, origin) {
			allowedOrigin = origin
			allowCredentials = true
		}

		if isPreflight {
			if allowedOrigin == "" {
				c.AbortWithStatus(consts.StatusForbidden)
				return
			}
			setCORSHeaders(c, allowedOrigin, allowCredentials)
			c.Header("Access-Control-Allow-Methods", corsAllowedMethods)
			c.Header("Access-Control-Allow-Headers", corsAllowedHeaders)
			c.Header("Access-Control-Max-Age", corsMaxAge)
			c.AbortWithStatus(consts.StatusNoContent)
			return
		}

		if allowedOrigin != "" {
			setCORSHeaders(c, allowedOrigin, allowCredentials)
		}
		c.Next(ctx)
	}
}

// setCORSHeaders 为实际请求/预检请求写入公共跨域响应头。
func setCORSHeaders(c *app.RequestContext, allowedOrigin string, allowCredentials bool) {
	c.Header("Access-Control-Allow-Origin", allowedOrigin)
	c.Header("Access-Control-Expose-Headers", corsExposedHeaders)
	if allowCredentials {
		c.Header("Access-Control-Allow-Credentials", "true")
	}
	// 回显来源时通知代理按 Origin 缓存，避免多个来源共享同一响应缓存。
	if allowedOrigin != "*" {
		c.Header("Vary", "Origin")
	}
}

// normalizeCORSOrigins 清理配置的来源列表：去空白、去空项、去重。
func normalizeCORSOrigins(origins []string) []string {
	seen := make(map[string]struct{}, len(origins))
	result := make([]string, 0, len(origins))
	for _, value := range origins {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

// containsCORSOrigin 判断列表中是否包含指定来源（区分大小写，Origin 大小写敏感）。
func containsCORSOrigin(origins []string, target string) bool {
	for _, value := range origins {
		if value == target {
			return true
		}
	}
	return false
}
