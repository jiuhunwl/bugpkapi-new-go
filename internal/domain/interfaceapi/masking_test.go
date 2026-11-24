package interfaceapi

import (
	stderrors "errors"
	"strings"
	"testing"

	apperrors "bugpk-api/internal/pkg/errors"

	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

func TestSanitizePublicInterfaceItemRemovesUpstreamConfiguration(t *testing.T) {
	got := sanitizePublicInterfaceItem(InterfaceItem{
		CurrentNode:          2,
		CurrentNodeURL:       "https://node-2.internal.example/weather/{city}",
		PollingCheckEnabled:  1,
		PollingCheckExpected: "ok",
		PollingCheckField:    "status",
		PollingEnabled:       1,
		PollingMode:          "FASTEST",
		RequestURL:           "https://primary.internal.example/weather/{city}",
		UpstreamConfigs:      []UpstreamConfig{{URL: "https://backup.internal.example"}},
		UpstreamCount:        2,
		UpstreamURLs:         []string{"https://backup.internal.example"},
		DomainRouting: DomainRoutingConfig{
			Enabled:        1,
			Fallback:       "REJECT",
			DefaultRouteID: "private",
			Routes:         []DomainPollingRoute{{ID: "private", RequestURL: "https://private.internal.example"}},
		},
	})
	if got.RequestURL != "" || got.CurrentNodeURL != "" || len(got.UpstreamConfigs) != 0 || len(got.UpstreamURLs) != 0 {
		t.Fatalf("public interface item leaked upstream configuration: %+v", got)
	}
	if got.PollingEnabled != 0 || got.PollingMode != "" || got.PollingCheckField != "" || got.PollingCheckExpected != "" || got.UpstreamCount != 0 {
		t.Fatalf("public interface item leaked polling internals: %+v", got)
	}
	if got.DomainRouting.Enabled != 0 || got.DomainRouting.Fallback != "" ||
		got.DomainRouting.DefaultRouteID != "" || len(got.DomainRouting.Routes) != 0 {
		t.Fatalf("public interface item leaked domain routing: %+v", got.DomainRouting)
	}
	if len(got.ParamKeys) != 1 || got.ParamKeys[0] != "city" {
		t.Fatalf("public interface item lost safe template parameter names: %+v", got.ParamKeys)
	}
}

func TestInvokeRequestParamsForLogMasksBeforePersisting(t *testing.T) {
	got := invokeRequestParamsForLog(
		map[string]any{
			"apiKey": "query-secret",
			"email":  "alice@example.com",
			"normal": "query-ok",
			"tags":   []any{"b", "a"},
		},
		map[string]any{
			"password": "pw",
			"nested": map[string]any{
				"mobile": "13800138000",
				"token":  "body-token",
			},
			"normal": "body-ok",
		},
	)

	for _, secret := range []string{"query-secret", "alice@example.com", `"pw"`, "body-token", "13800138000"} {
		if strings.Contains(got, secret) {
			t.Fatalf("invoke request log leaked %q: %s", secret, got)
		}
	}
	for _, safe := range []string{"query-ok", "body-ok", "138****8000", "[REDACTED]", `"tags":["a","b"]`} {
		if !strings.Contains(got, safe) {
			t.Fatalf("invoke request log missing %q: %s", safe, got)
		}
	}
}

func TestInvokeRequestParamsForLogParsesFormAndRedactsRawBody(t *testing.T) {
	form := invokeRequestParamsForLog(nil, "password=pw&normal=ok&mobile=13800138000")
	for _, secret := range []string{"pw", "13800138000"} {
		if strings.Contains(form, secret) {
			t.Fatalf("form request log leaked %q: %s", secret, form)
		}
	}
	if !strings.Contains(form, "ok") || !strings.Contains(form, "138****8000") {
		t.Fatalf("form request log did not preserve safe values: %s", form)
	}

	plain := invokeRequestParamsForLog(nil, "raw token secret text")
	if strings.Contains(plain, "raw token secret text") {
		t.Fatalf("plain request body should not be persisted: %s", plain)
	}
	if !strings.Contains(plain, "[NON_STRUCTURED_BODY_REDACTED]") {
		t.Fatalf("plain request body did not use redaction marker: %s", plain)
	}
}

func TestRequestDisplayMasksBeforePersisting(t *testing.T) {
	queryDisplay := requestDisplay(map[string]any{"key": "secret", "mobile": "13800138000", "normal": "ok"}, nil)
	if strings.Contains(queryDisplay, "secret") || strings.Contains(queryDisplay, "13800138000") {
		t.Fatalf("query request display leaked secret: %s", queryDisplay)
	}
	if !strings.Contains(queryDisplay, "ok") || !strings.Contains(queryDisplay, "138****8000") {
		t.Fatalf("query request display did not keep safe values: %s", queryDisplay)
	}

	bodyDisplay := requestDisplay(nil, `{"token":"body-token","normal":"ok"}`)
	if strings.Contains(bodyDisplay, "body-token") {
		t.Fatalf("body request display leaked secret: %s", bodyDisplay)
	}
	if !strings.Contains(bodyDisplay, "ok") || !strings.Contains(bodyDisplay, "[REDACTED]") {
		t.Fatalf("body request display did not keep safe values: %s", bodyDisplay)
	}
}

func TestApplyTemplateMissingParameterReturnsBadRequest(t *testing.T) {
	_, err := applyTemplate("https://example.com/weather/{city}", map[string]any{})
	if err == nil {
		t.Fatal("expected missing parameter to return an error")
	}
	var business *apperrors.BusinessError
	if !stderrors.As(err, &business) {
		t.Fatalf("expected BusinessError, got %T", err)
	}
	if business.StatusCode != consts.StatusBadRequest {
		t.Fatalf("status = %d, want %d", business.StatusCode, consts.StatusBadRequest)
	}
}

func TestPublicInvokeFailureDoesNotExposeUpstreamDetails(t *testing.T) {
	internalURL := "https://admin:secret@upstream.internal.example:9443/private/main?token=raw-token"
	result := publicInvokeFailure(InvokeResult{
		Body:        "dial " + internalURL,
		StatusCode:  consts.StatusBadGateway,
		UpstreamURL: internalURL,
	}, stderrors.New("Get \""+internalURL+"\": connection refused"))

	if result.UpstreamURL != "" || result.Success {
		t.Fatalf("public failure retained upstream state: %+v", result)
	}
	for _, secret := range []string{"upstream.internal.example", "9443", "/private/main", "raw-token", "admin", "secret"} {
		if strings.Contains(result.Body, secret) {
			t.Fatalf("public failure body leaked %q: %s", secret, result.Body)
		}
	}
	if !strings.Contains(result.Body, "服务暂时不可用") {
		t.Fatalf("public failure body is not the controlled response: %s", result.Body)
	}
}

func TestPublicInvokeFailureKeepsSafeClientError(t *testing.T) {
	err := apperrors.New(consts.StatusBadRequest, "缺少接口参数: city")
	result := publicInvokeFailure(InvokeResult{StatusCode: consts.StatusBadRequest, UpstreamURL: "https://internal.example/{city}"}, err)
	if result.UpstreamURL != "" || !strings.Contains(result.Body, "缺少接口参数: city") {
		t.Fatalf("safe client error was not preserved: %+v", result)
	}
}

func TestPublicInvokeFailureRedactsUpstreamURLFromBusinessMessage(t *testing.T) {
	err := apperrors.New(consts.StatusBadRequest, `上游参数错误: https://parse.upstream.example.com/api/parse?url=https%3A%2F%2Fv.kuaishou.com%2FAbCdEf12`)
	result := publicInvokeFailure(InvokeResult{StatusCode: consts.StatusBadRequest}, err)
	for _, forbidden := range []string{"parse.upstream.example.com", "v.kuaishou.com", "AbCdEf12", "/api/parse"} {
		if strings.Contains(result.Body, forbidden) {
			t.Fatalf("public business error leaked upstream detail %q: %s", forbidden, result.Body)
		}
	}
	if !strings.Contains(result.Body, "[UPSTREAM_REDACTED]") {
		t.Fatalf("public business error lost the redaction marker: %s", result.Body)
	}
}
