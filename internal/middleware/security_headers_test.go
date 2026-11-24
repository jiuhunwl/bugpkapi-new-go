package middleware

import (
	"context"
	"strings"
	"testing"

	hertzapp "github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/common/config"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"github.com/cloudwego/hertz/pkg/route"
)

func TestSecurityHeaders(t *testing.T) {
	engine := route.NewEngine(config.NewOptions([]config.Option{}))
	engine.Use(SecurityHeaders())
	engine.GET("/ok", func(ctx context.Context, c *hertzapp.RequestContext) {
		c.String(consts.StatusOK, "ok")
	})

	response := ut.PerformRequest(engine, consts.MethodGet, "/ok", nil)
	headers := map[string]string{
		"Permissions-Policy":                "camera=(), microphone=(), geolocation=()",
		"Referrer-Policy":                   "no-referrer",
		"X-Content-Type-Options":            "nosniff",
		"X-Frame-Options":                   "DENY",
		"X-Permitted-Cross-Domain-Policies": "none",
	}
	for key, want := range headers {
		if got := response.Header().Get(key); got != want {
			t.Fatalf("%s = %q, want %q", key, got, want)
		}
	}
}

func TestSecurityHeadersProtectSystemAPIFromCaching(t *testing.T) {
	engine := route.NewEngine(config.NewOptions([]config.Option{}))
	engine.Use(SecurityHeaders("/console-api"))
	engine.GET("/console-api/auth/login", func(ctx context.Context, c *hertzapp.RequestContext) {
		c.String(consts.StatusOK, "ok")
	})

	response := ut.PerformRequest(engine, consts.MethodGet, "/console-api/auth/login", nil)
	headers := map[string]string{
		"Cache-Control":           "no-store, max-age=0",
		"Content-Security-Policy": systemAPICSP,
		"Expires":                 "0",
		"Pragma":                  "no-cache",
	}
	for key, want := range headers {
		if got := response.Header().Get(key); got != want {
			t.Fatalf("%s = %q, want %q", key, got, want)
		}
	}
}

func TestSecurityHeadersUseImmutableUploadCachingAndDisableOpenGatewayCaching(t *testing.T) {
	engine := route.NewEngine(config.NewOptions([]config.Option{}))
	engine.Use(SecurityHeaders("/system-api"))
	engine.GET("/system-api/upload/site-logo/:filename", func(ctx context.Context, c *hertzapp.RequestContext) {
		c.String(consts.StatusOK, "image")
	})
	engine.GET("/api/:apiCode", func(ctx context.Context, c *hertzapp.RequestContext) {
		c.String(consts.StatusOK, "open")
	})

	upload := ut.PerformRequest(engine, consts.MethodGet, "/system-api/upload/site-logo/logo.png", nil)
	if got := upload.Header().Get("Cache-Control"); got != immutableUploadCache {
		t.Fatalf("upload Cache-Control = %q, want %q", got, immutableUploadCache)
	}

	open := ut.PerformRequest(engine, consts.MethodGet, "/api/weather", nil)
	if got := open.Header().Get("Cache-Control"); got != "no-store, max-age=0" {
		t.Fatalf("open gateway Cache-Control = %q", got)
	}
	if open.Header().Get("Pragma") != "no-cache" || open.Header().Get("Expires") != "0" {
		t.Fatal("open gateway response is missing legacy no-cache headers")
	}
	if got := open.Header().Get("Content-Security-Policy"); got != openGatewayCSP {
		t.Fatalf("open gateway CSP = %q", got)
	}
}

func TestSecurityHeadersDoNotCacheUploadMutationsOrErrors(t *testing.T) {
	engine := route.NewEngine(config.NewOptions([]config.Option{}))
	engine.Use(SecurityHeaders("/system-api"))
	engine.POST("/system-api/upload/site-logo", func(ctx context.Context, c *hertzapp.RequestContext) {
		c.String(consts.StatusOK, "uploaded")
	})
	engine.GET("/system-api/upload/site-logo/:filename", func(ctx context.Context, c *hertzapp.RequestContext) {
		c.String(consts.StatusNotFound, "missing")
	})

	mutation := ut.PerformRequest(engine, consts.MethodPost, "/system-api/upload/site-logo", nil)
	if got := mutation.Header().Get("Cache-Control"); got != "no-store, max-age=0" {
		t.Fatalf("upload mutation Cache-Control = %q", got)
	}
	errorResponse := ut.PerformRequest(engine, consts.MethodGet, "/system-api/upload/site-logo/missing.png", nil)
	if got := errorResponse.Header().Get("Cache-Control"); got != "no-store, max-age=0" {
		t.Fatalf("upload error Cache-Control = %q", got)
	}
}

func TestSecurityHeadersDisableProbeCaching(t *testing.T) {
	engine := route.NewEngine(config.NewOptions([]config.Option{}))
	engine.Use(SecurityHeaders("/system-api"))
	for _, path := range []string{"/healthz", "/readyz"} {
		engine.GET(path, func(ctx context.Context, c *hertzapp.RequestContext) {
			c.String(consts.StatusOK, "ok")
		})
	}

	for _, path := range []string{"/healthz", "/readyz"} {
		response := ut.PerformRequest(engine, consts.MethodGet, path, nil)
		if got := response.Header().Get("Cache-Control"); got != "no-store, max-age=0" {
			t.Fatalf("%s Cache-Control = %q", path, got)
		}
		if response.Header().Get("Pragma") != "no-cache" || response.Header().Get("Expires") != "0" {
			t.Fatalf("%s is missing legacy no-cache headers", path)
		}
		if got := response.Header().Get("Content-Security-Policy"); got != systemAPICSP {
			t.Fatalf("%s CSP = %q", path, got)
		}
	}
}

func TestOpenGatewayCSPSandboxesActiveUpstreamContent(t *testing.T) {
	if !strings.Contains(openGatewayCSP, "sandbox") || !strings.Contains(openGatewayCSP, "default-src 'none'") {
		t.Fatalf("open gateway CSP does not sandbox active content: %q", openGatewayCSP)
	}
	for _, unsafe := range []string{"allow-scripts", "allow-same-origin", "script-src"} {
		if strings.Contains(openGatewayCSP, unsafe) {
			t.Fatalf("open gateway CSP contains unsafe capability %q", unsafe)
		}
	}
}

func TestSecurityHeadersOverrideDownstreamWeakening(t *testing.T) {
	engine := route.NewEngine(config.NewOptions([]config.Option{}))
	engine.Use(SecurityHeaders("/system-api"))
	engine.GET("/system-api/auth/refresh", func(ctx context.Context, c *hertzapp.RequestContext) {
		c.Response.Header.Set("Cache-Control", "public, max-age=3600")
		c.Response.Header.Set("Content-Security-Policy", "default-src * 'unsafe-inline'")
		c.Response.Header.Set("X-Frame-Options", "ALLOWALL")
		c.String(consts.StatusOK, "token")
	})

	response := ut.PerformRequest(engine, consts.MethodGet, "/system-api/auth/refresh", nil)
	if got := response.Header().Get("Cache-Control"); got != "no-store, max-age=0" {
		t.Fatalf("final Cache-Control = %q", got)
	}
	if got := response.Header().Get("Content-Security-Policy"); got != systemAPICSP {
		t.Fatalf("final Content-Security-Policy = %q", got)
	}
	if got := response.Header().Get("X-Frame-Options"); got != "DENY" {
		t.Fatalf("final X-Frame-Options = %q", got)
	}
}

func TestWithinPathPrefixRequiresPathBoundary(t *testing.T) {
	if !withinPathPrefix("/system-api/user", "/system-api") {
		t.Fatal("expected child path to match prefix")
	}
	if withinPathPrefix("/system-api-evil/user", "/system-api") {
		t.Fatal("prefix match must require a path boundary")
	}
}
