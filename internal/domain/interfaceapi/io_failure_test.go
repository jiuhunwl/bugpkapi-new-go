package interfaceapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"

	"bugpk-api/internal/pkg/httpclient"

	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

type interfaceRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn interfaceRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

type interfaceFailingReadCloser struct{}

func (interfaceFailingReadCloser) Read([]byte) (int, error) {
	return 0, errors.New("management upstream body read failed")
}

func (interfaceFailingReadCloser) Close() error { return nil }

func TestManagementInvokeTreatsResponseReadFailureAsBadGateway(t *testing.T) {
	service := &Service{httpClient: &http.Client{Transport: interfaceRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: interfaceFailingReadCloser{}}, nil
	})}}
	result, err := service.invokeOne(context.Background(), InterfaceItem{}, http.MethodGet, UpstreamConfig{URL: "https://upstream.example/test"}, InvokePayload{})
	if err == nil || !strings.Contains(err.Error(), "read upstream response") {
		t.Fatalf("expected response read error, got result=%+v err=%v", result, err)
	}
	if result.StatusCode != consts.StatusBadGateway || result.Success {
		t.Fatalf("read failure must be a failed gateway result: %+v", result)
	}
}

func TestManagementInvokeRejectsOversizedUpstreamResponse(t *testing.T) {
	body := strings.Repeat("x", int(httpclient.MaxUpstreamResponseBytes)+1)
	service := &Service{httpClient: &http.Client{Transport: interfaceRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}}
	result, err := service.invokeOne(context.Background(), InterfaceItem{}, http.MethodGet, UpstreamConfig{URL: "https://upstream.example/test"}, InvokePayload{})
	if !errors.Is(err, httpclient.ErrResponseTooLarge) {
		t.Fatalf("oversized response error = %v, want ErrResponseTooLarge", err)
	}
	if result.StatusCode != consts.StatusBadGateway || result.Success {
		t.Fatalf("oversized response must be a failed gateway result: %+v", result)
	}
}

func TestTruncateRunesKeepsValidUTF8(t *testing.T) {
	if got := truncateRunes("接口响应正常", 4); got != "接口响应" {
		t.Fatalf("truncateRunes() = %q", got)
	}
}

func TestManagementResponseLogUsesByteLimitAndOriginalSize(t *testing.T) {
	original := strings.Repeat("响", 8000)
	saved := responseBodyForLog(original)
	if len(saved) > 20000 {
		t.Fatalf("saved response bytes = %d, want at most 20000", len(saved))
	}
	if responseSizeForInvokeLog(InvokeResult{Body: original, ResponseSize: int64(len(original))}) != int64(len(original)) {
		t.Fatal("management log did not preserve the original response size")
	}
	if responseSizeForInvokeLog(InvokeResult{Body: "fallback"}) != 8 {
		t.Fatal("management log response size fallback is incorrect")
	}
}

func TestManagementResponseTruncationFlagIgnoresMaskingLengthChanges(t *testing.T) {
	body := `{"refreshToken":"refresh-secret","data":"ok"}`
	if responseWasTruncated(body, int64(len(body))) {
		t.Fatal("complete management response was marked truncated before masking")
	}
	if !responseWasTruncated(strings.Repeat("x", 20001), 20001) {
		t.Fatal("oversized management response was not marked truncated")
	}
}

func TestManagementResponseLogOmitsInvalidUTF8(t *testing.T) {
	if got := responseBodyForLog(string([]byte{0xff, 0xfe})); got != "[BINARY_RESPONSE_OMITTED]" {
		t.Fatalf("binary response log = %q", got)
	}
}

// 后台接口调试落库同样保留业务直链：它记录的是调用方视角的响应内容，
// 上游路由地址由 upstream_url 列单独保护。
func TestManagementResponseLogKeepsBusinessLinks(t *testing.T) {
	got := responseBodyForLog(`{"data":{"video_url":"https://v11-vdl.doubao.com/abc/video.mp4"}}`)
	if !strings.Contains(got, "v11-vdl.doubao.com/abc/video.mp4") {
		t.Fatalf("business link lost: %s", got)
	}
	if strings.Contains(got, "[UPSTREAM_REDACTED]") {
		t.Fatalf("management response was endpoint-redacted: %s", got)
	}
}

func TestManagementResponseLogMasksSecretsBeforePersistence(t *testing.T) {
	got := responseBodyForLog(`{"refreshToken":"refresh-secret","apiKey":"key-secret","email":"alice@example.com","data":"ok"}`)
	for _, secret := range []string{"refresh-secret", "key-secret", "alice@example.com"} {
		if strings.Contains(got, secret) {
			t.Fatalf("response log leaked %q: %s", secret, got)
		}
	}
	if !strings.Contains(got, `"data":"ok"`) {
		t.Fatalf("non-sensitive response content was lost: %s", got)
	}
}

func TestManagementDiagnosticTextMasksSecretsBeforePersistence(t *testing.T) {
	got := safeDiagnosticText("invoke https://upstream.example/path?token=raw-token failed password=secret-password", 500)
	if strings.Contains(got, "raw-token") || strings.Contains(got, "secret-password") {
		t.Fatalf("management diagnostic leaked credentials: %s", got)
	}
	if !strings.Contains(got, "invoke") {
		t.Fatalf("diagnostic context was lost: %s", got)
	}
}

func TestManagementRequestLogFieldsAreMaskedAndBounded(t *testing.T) {
	params := requestParamsForPersistence(`{"password":"secret-password","data":"` + strings.Repeat("管", 8000) + `"}`)
	if len(params) > 20000 || !utf8.ValidString(params) {
		t.Fatalf("request params bytes=%d valid=%v", len(params), utf8.ValidString(params))
	}
	if strings.Contains(params, "secret-password") {
		t.Fatalf("request params leaked a password: %s", params)
	}

	value := requestValueForPersistence("https://example.com/path?apiKey=secret-key&data=" + strings.Repeat("x", 800))
	if len(value) > 500 || !utf8.ValidString(value) {
		t.Fatalf("request value bytes=%d valid=%v", len(value), utf8.ValidString(value))
	}
	if strings.Contains(value, "secret-key") {
		t.Fatalf("request value leaked an API key: %s", value)
	}
}

func TestInvokeRequestSizeUsesSerializedQueryAndBodyBytes(t *testing.T) {
	query := map[string]any{"city": "上海", "page": 2}
	body := map[string]any{"enabled": true, "name": "测试"}
	queryJSON, _ := json.Marshal(query)
	bodyJSON, _ := json.Marshal(body)
	want := int64(len(queryJSON) + len(bodyJSON))
	if got := invokeRequestSize(query, body); got != want {
		t.Fatalf("invokeRequestSize() = %d, want %d", got, want)
	}
	if got := invokeRequestSize(nil, nil); got != 0 {
		t.Fatalf("empty invokeRequestSize() = %d", got)
	}
}
