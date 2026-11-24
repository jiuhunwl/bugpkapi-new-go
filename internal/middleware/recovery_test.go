package middleware

import (
	"context"
	"fmt"
	"strings"
	"testing"

	hertzapp "github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/common/config"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"github.com/cloudwego/hertz/pkg/route"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestRecoveryReturnsGenericErrorAndLogsStack(t *testing.T) {
	core, logs := observer.New(zap.ErrorLevel)
	engine := route.NewEngine(config.NewOptions([]config.Option{}))
	engine.Use(Recovery(zap.New(core)))
	engine.GET("/system-api/failure", func(ctx context.Context, c *hertzapp.RequestContext) {
		panic("runtime-failure")
	})

	response := ut.PerformRequest(engine, consts.MethodGet, "/system-api/failure", nil)
	if response.Code != consts.StatusInternalServerError {
		t.Fatalf("status = %d", response.Code)
	}
	body := response.Body.String()
	if strings.Contains(body, "runtime-failure") || !strings.Contains(body, `"code":500`) || !strings.Contains(body, "error_id") {
		t.Fatalf("unexpected recovery response: %s", body)
	}
	entries := logs.All()
	if len(entries) != 1 {
		t.Fatalf("recovery log count = %d", len(entries))
	}
	fields := entries[0].ContextMap()
	if fields["path"] != "/system-api/failure" || !strings.Contains(fmt.Sprint(fields["stack"]), "TestRecoveryReturnsGenericErrorAndLogsStack") {
		t.Fatalf("recovery diagnostics missing: %+v", fields)
	}
}

func TestRecoveryAcceptsNilLogger(t *testing.T) {
	engine := route.NewEngine(config.NewOptions([]config.Option{}))
	engine.Use(Recovery(nil))
	engine.GET("/failure", func(ctx context.Context, c *hertzapp.RequestContext) {
		panic("runtime-failure")
	})

	response := ut.PerformRequest(engine, consts.MethodGet, "/failure", nil)
	if response.Code != consts.StatusInternalServerError {
		t.Fatalf("status = %d", response.Code)
	}
}
