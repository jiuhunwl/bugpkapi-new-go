package middleware

import (
	"context"
	"strings"

	hertzapp "github.com/cloudwego/hertz/pkg/app"
)

const systemAPICSP = "default-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'"
const openGatewayCSP = "default-src 'none'; sandbox; base-uri 'none'; frame-ancestors 'none'; form-action 'none'"
const immutableUploadCache = "public, max-age=31536000, immutable"

func SecurityHeaders(systemPrefixes ...string) hertzapp.HandlerFunc {
	systemPrefix := "/system-api"
	if len(systemPrefixes) > 0 && strings.TrimSpace(systemPrefixes[0]) != "" {
		systemPrefix = strings.TrimRight(strings.TrimSpace(systemPrefixes[0]), "/")
	}
	return func(ctx context.Context, c *hertzapp.RequestContext) {
		applySecurityHeaders(c, systemPrefix)
		c.Next(ctx)
		// Reapply after downstream handlers so neither application code nor
		// untrusted upstream response metadata can weaken the final policy.
		applySecurityHeaders(c, systemPrefix)
	}
}

func applySecurityHeaders(c *hertzapp.RequestContext, systemPrefix string) {
	c.Response.Header.Set("X-Content-Type-Options", "nosniff")
	c.Response.Header.Set("X-Frame-Options", "DENY")
	c.Response.Header.Set("Referrer-Policy", "no-referrer")
	c.Response.Header.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
	c.Response.Header.Set("X-Permitted-Cross-Domain-Policies", "none")

	path := string(c.Path())
	if path == "/healthz" || path == "/readyz" {
		setNoStore(c)
		c.Response.Header.Set("Content-Security-Policy", systemAPICSP)
		return
	}
	if withinPathPrefix(path, systemPrefix) {
		c.Response.Header.Set("Content-Security-Policy", systemAPICSP)
		if withinPathPrefix(path, systemPrefix+"/upload") {
			applyUploadCachePolicy(c)
			return
		}
		setNoStore(c)
	} else if withinPathPrefix(path, "/api") {
		setNoStore(c)
		c.Response.Header.Set("Content-Security-Policy", openGatewayCSP)
	}
}

func applyUploadCachePolicy(c *hertzapp.RequestContext) {
	method := strings.ToUpper(strings.TrimSpace(string(c.Method())))
	status := c.Response.StatusCode()
	if (method == "GET" || method == "HEAD") && status >= 200 && status < 300 {
		c.Response.Header.Set("Cache-Control", immutableUploadCache)
		c.Response.Header.Del("Pragma")
		c.Response.Header.Del("Expires")
		return
	}
	setNoStore(c)
}

func setNoStore(c *hertzapp.RequestContext) {
	c.Response.Header.Set("Cache-Control", "no-store, max-age=0")
	c.Response.Header.Set("Pragma", "no-cache")
	c.Response.Header.Set("Expires", "0")
}

func withinPathPrefix(path, prefix string) bool {
	return path == prefix || strings.HasPrefix(path, prefix+"/")
}
