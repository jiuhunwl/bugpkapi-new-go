package middleware

import (
	"context"
	"testing"

	"bugpk-api/internal/pkg/i18n"

	hertzapp "github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/common/config"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"github.com/cloudwego/hertz/pkg/route"
)

type fixedLocaleResolver struct{}

func (fixedLocaleResolver) Resolve(explicit, appLocale, acceptLanguage string) string {
	return explicit
}

func TestLocaleMiddlewareStoresResolutionAndHeaders(t *testing.T) {
	engine := route.NewEngine(config.NewOptions([]config.Option{}))
	engine.Use(Locale(fixedLocaleResolver{}))
	engine.GET("/probe", func(ctx context.Context, c *hertzapp.RequestContext) {
		c.String(consts.StatusOK, i18n.LocaleFromContext(ctx))
	})

	response := ut.PerformRequest(engine, consts.MethodGet, "/probe?lang=en-US", nil)
	if got := response.Body.String(); got != "en-US" {
		t.Fatalf("context locale = %q, want en-US", got)
	}
	if got := response.Header().Get("Content-Language"); got != "en-US" {
		t.Fatalf("Content-Language = %q", got)
	}
	if got := response.Header().Get("Vary"); got != "X-App-Locale, Accept-Language" {
		t.Fatalf("Vary = %q", got)
	}
}
