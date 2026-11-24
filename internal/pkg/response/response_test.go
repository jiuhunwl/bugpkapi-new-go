package response

import (
	"context"
	"encoding/json"
	stderrors "errors"
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

	"bugpk-api/internal/pkg/accountstatus"
	apperrors "bugpk-api/internal/pkg/errors"
)

func TestBodyShape(t *testing.T) {
	body := Body{
		Code:    0,
		Data:    map[string]string{"status": "ok"},
		Error:   nil,
		Message: "ok",
	}

	if body.Code != 0 || body.Message != "ok" || body.Error != nil {
		t.Fatalf("unexpected success body: %+v", body)
	}
}

func TestFromErrorPreservesStructuredBanContract(t *testing.T) {
	engine := route.NewEngine(config.NewOptions([]config.Option{}))
	engine.GET("/banned", func(ctx context.Context, c *hertzapp.RequestContext) {
		FromError(c, accountstatus.Forbidden(0, "2026-08-12 10:00:00", "repeated abuse"))
	})

	result := ut.PerformRequest(engine, consts.MethodGet, "/banned", nil)
	if result.Code != consts.StatusForbidden {
		t.Fatalf("status = %d", result.Code)
	}
	for name, want := range map[string]string{
		"X-Bugpk-Account-Status": "BANNED",
		"X-Bugpk-Ban-Type":       "TEMPORARY",
		"X-Bugpk-Ban-Until":      "2026-08-12 10:00:00",
	} {
		if got := result.Header().Get(name); got != want {
			t.Fatalf("%s = %q, want %q", name, got, want)
		}
	}
	var body struct {
		Code    int                      `json:"code"`
		Data    accountstatus.BanDetails `json:"data"`
		Message string                   `json:"message"`
	}
	if err := json.Unmarshal(result.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Code != -1 || body.Data.Tag != accountstatus.BanTag || body.Data.BanReason != "repeated abuse" || body.Data.ResourceAccess != accountstatus.ResourceAccessDeny {
		t.Fatalf("unexpected ban response: %+v", body)
	}
	if !strings.Contains(body.Message, body.Data.BanReason) {
		t.Fatalf("ban reason missing from response message: %+v", body)
	}
}

func TestFromErrorLogsUnhandledErrorWithoutExposingIt(t *testing.T) {
	core, logs := observer.New(zap.ErrorLevel)
	SetLogger(zap.New(core))
	t.Cleanup(func() { SetLogger(nil) })
	engine := route.NewEngine(config.NewOptions([]config.Option{}))
	engine.GET("/system-api/test", func(ctx context.Context, c *hertzapp.RequestContext) {
		FromError(c, stderrors.New("database password=secret"))
	})

	response := ut.PerformRequest(engine, consts.MethodGet, "/system-api/test", nil)
	if response.Code != consts.StatusInternalServerError {
		t.Fatalf("status = %d", response.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["message"] != "服务暂时不可用" {
		t.Fatalf("unexpected 5xx message: %+v", body)
	}
	if body["error_id"] == nil || body["error_id"] == "" {
		t.Fatalf("5xx response must carry error_id: %+v", body)
	}
	if _, ok := body["error"]; ok {
		t.Fatalf("5xx response must not carry raw error field: %+v", body)
	}
	raw := response.Body.String()
	if strings.Contains(raw, "database") || strings.Contains(raw, "secret") {
		t.Fatalf("internal error leaked in response: %s", raw)
	}
	entries := logs.All()
	if len(entries) != 1 {
		t.Fatalf("unexpected error log count: %d", len(entries))
	}
	loggedError := fmt.Sprint(entries[0].ContextMap()["error"])
	if strings.Contains(loggedError, "secret") || !strings.Contains(loggedError, "[REDACTED]") {
		t.Fatalf("unexpected error logs: %+v", entries)
	}
	if entries[0].ContextMap()["path"] != "/system-api/test" {
		t.Fatalf("path field missing: %+v", entries[0].ContextMap())
	}
	if entries[0].ContextMap()["error_id"] != body["error_id"] {
		t.Fatalf("error_id mismatch between response and log: %+v", entries[0].ContextMap())
	}
}

func TestFromErrorLogsWrappedServerErrorButNotClientError(t *testing.T) {
	core, logs := observer.New(zap.ErrorLevel)
	SetLogger(zap.New(core))
	t.Cleanup(func() { SetLogger(nil) })
	engine := route.NewEngine(config.NewOptions([]config.Option{}))
	engine.GET("/server", func(ctx context.Context, c *hertzapp.RequestContext) {
		FromError(c, apperrors.Wrap(consts.StatusServiceUnavailable, "服务暂不可用", stderrors.New("redis timeout")))
	})
	engine.GET("/client", func(ctx context.Context, c *hertzapp.RequestContext) {
		FromError(c, apperrors.Wrap(consts.StatusBadRequest, "请求参数不合法", stderrors.New("json syntax")))
	})

	serverResponse := ut.PerformRequest(engine, consts.MethodGet, "/server", nil)
	clientResponse := ut.PerformRequest(engine, consts.MethodGet, "/client", nil)
	if serverResponse.Code != consts.StatusServiceUnavailable || clientResponse.Code != consts.StatusBadRequest {
		t.Fatalf("unexpected statuses: server=%d client=%d", serverResponse.Code, clientResponse.Code)
	}
	if strings.Contains(serverResponse.Body.String(), "redis timeout") {
		t.Fatalf("wrapped dependency error leaked in response: %s", serverResponse.Body.Bytes())
	}
	var serverBody map[string]any
	if err := json.Unmarshal(serverResponse.Body.Bytes(), &serverBody); err != nil {
		t.Fatal(err)
	}
	if serverBody["message"] != "服务暂时不可用" || serverBody["error_id"] == "" {
		t.Fatalf("unexpected 5xx body: %+v", serverBody)
	}
	entries := logs.All()
	if len(entries) != 1 || !strings.Contains(fmt.Sprint(entries[0].ContextMap()["error"]), "redis timeout") {
		t.Fatalf("unexpected server error logs: %+v", entries)
	}
	if entries[0].ContextMap()["error_id"] != serverBody["error_id"] {
		t.Fatalf("error_id mismatch between response and log: %+v", entries[0].ContextMap())
	}
}

func TestServerErrorBodiesDoNotExposeUpstreamFor502And504(t *testing.T) {
	core, _ := observer.New(zap.ErrorLevel)
	SetLogger(zap.New(core))
	t.Cleanup(func() { SetLogger(nil) })
	engine := route.NewEngine(config.NewOptions([]config.Option{}))
	engine.GET("/bad-gateway", func(ctx context.Context, c *hertzapp.RequestContext) {
		FailServerError(c, consts.StatusBadGateway, stderrors.New("GET https://internal-upstream.example.com/secret/api?token=abc123: dial tcp refused"))
	})
	engine.GET("/gateway-timeout", func(ctx context.Context, c *hertzapp.RequestContext) {
		FailServerError(c, consts.StatusGatewayTimeout, stderrors.New("upstream /v1/internal/order timeout"))
	})

	for _, tc := range []struct {
		path   string
		status int
	}{
		{path: "/bad-gateway", status: consts.StatusBadGateway},
		{path: "/gateway-timeout", status: consts.StatusGatewayTimeout},
	} {
		response := ut.PerformRequest(engine, consts.MethodGet, tc.path, nil)
		if response.Code != tc.status {
			t.Fatalf("%s status = %d, want %d", tc.path, response.Code, tc.status)
		}
		raw := response.Body.String()
		if strings.Contains(raw, "internal-upstream") || strings.Contains(raw, "/v1/internal/order") || strings.Contains(raw, "token=abc123") {
			t.Fatalf("%s leaked upstream details: %s", tc.path, raw)
		}
		var body map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body["message"] != "服务暂时不可用" || body["error_id"] == "" {
			t.Fatalf("%s unexpected body: %+v", tc.path, body)
		}
	}
}

func TestServerErrorLogsRedactUpstreamURLFromWrappedHTTPError(t *testing.T) {
	core, logs := observer.New(zap.ErrorLevel)
	SetLogger(zap.New(core))
	t.Cleanup(func() { SetLogger(nil) })
	engine := route.NewEngine(config.NewOptions([]config.Option{}))
	engine.GET("/upstream-timeout", func(ctx context.Context, c *hertzapp.RequestContext) {
		FailServerError(c, consts.StatusBadGateway, stderrors.New(`Get "https://parse.upstream.example.com/api/parse?url=https%3A%2F%2Fv.kuaishou.com%2FAbCdEf12": net/http: timeout awaiting response headers`))
	})

	result := ut.PerformRequest(engine, consts.MethodGet, "/upstream-timeout", nil)
	if result.Code != consts.StatusBadGateway {
		t.Fatalf("status = %d", result.Code)
	}
	if strings.Contains(result.Body.String(), "parse.upstream.example.com") || strings.Contains(result.Body.String(), "v.kuaishou.com") {
		t.Fatalf("upstream URL leaked in response: %s", result.Body.String())
	}
	entries := logs.All()
	if len(entries) != 1 {
		t.Fatalf("unexpected error log count: %d", len(entries))
	}
	loggedError := fmt.Sprint(entries[0].ContextMap()["error"])
	for _, forbidden := range []string{"parse.upstream.example.com", "v.kuaishou.com", "AbCdEf12", "/api/parse"} {
		if strings.Contains(loggedError, forbidden) {
			t.Fatalf("upstream detail leaked in error log (%q): %s", forbidden, loggedError)
		}
	}
	if !strings.Contains(loggedError, "timeout awaiting response headers") {
		t.Fatalf("safe timeout context missing from error log: %s", loggedError)
	}
}

func TestClientErrorMessageRedactsEmbeddedUpstreamURL(t *testing.T) {
	engine := route.NewEngine(config.NewOptions([]config.Option{}))
	engine.GET("/client-url-error", func(ctx context.Context, c *hertzapp.RequestContext) {
		FromError(c, apperrors.New(consts.StatusBadRequest, `invalid upstream https://parse.upstream.example.com/api/parse?url=https%3A%2F%2Fv.kuaishou.com%2FAbCdEf12`))
	})

	result := ut.PerformRequest(engine, consts.MethodGet, "/client-url-error", nil)
	if result.Code != consts.StatusBadRequest {
		t.Fatalf("status = %d", result.Code)
	}
	for _, forbidden := range []string{"parse.upstream.example.com", "v.kuaishou.com", "AbCdEf12", "/api/parse"} {
		if strings.Contains(result.Body.String(), forbidden) {
			t.Fatalf("upstream detail leaked in client error response (%q): %s", forbidden, result.Body.String())
		}
	}
}
