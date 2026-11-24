package observability

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMaskCallLogItemMasksSensitiveResponseFields(t *testing.T) {
	got := maskCallLogItem(CallLogItem{
		ClientIP:      "192.168.1.100",
		RequestParams: `{"query":{"key":["secret"],"email":["alice@example.com"]},"body":{"password":"pw","normal":"ok"}}`,
		RequestValue:  "https://example.com/api?token=secret&mobile=13800138000",
		ResponseBody:  `{"accessToken":"abc","data":"ok"}`,
		UpstreamURL:   "https://upstream.example.com/path?api_key=secret",
		ErrorMessage:  "upstream password=secret-password token=raw-token",
	})
	if got.ClientIP != "192.168.*.*" {
		t.Fatalf("client ip = %q", got.ClientIP)
	}
	if strings.Contains(got.RequestParams, "secret") || strings.Contains(got.RequestParams, `"pw"`) {
		t.Fatalf("request params not masked: %s", got.RequestParams)
	}
	if strings.Contains(got.RequestValue, "secret") || !strings.Contains(got.RequestValue, "138%2A%2A%2A%2A8000") {
		t.Fatalf("request value not masked: %s", got.RequestValue)
	}
	if !strings.Contains(got.RequestValue, "example.com/api") {
		t.Fatalf("caller URL was incorrectly erased: %s", got.RequestValue)
	}
	if strings.Contains(got.ResponseBody, "abc") {
		t.Fatalf("response body not masked: %s", got.ResponseBody)
	}
	if strings.Contains(got.UpstreamURL, "secret") {
		t.Fatalf("upstream url not masked: %s", got.UpstreamURL)
	}
	if strings.Contains(got.ErrorMessage, "secret-password") || strings.Contains(got.ErrorMessage, "raw-token") {
		t.Fatalf("error message not masked: %s", got.ErrorMessage)
	}
}

func TestMaskCallLogItemNeverExposesUpstreamEndpoint(t *testing.T) {
	got := maskCallLogItem(CallLogItem{
		UpstreamURL: "https://primary.example/internal/parse?url=https%3A%2F%2Fprivate.example/item",
	})
	for _, forbidden := range []string{"primary.example", "private.example", "/internal/parse", "url="} {
		if strings.Contains(got.UpstreamURL, forbidden) {
			t.Fatalf("call-log DTO leaked endpoint detail %q: %s", forbidden, got.UpstreamURL)
		}
	}
}

func TestUnmaskedCallLogFieldsPreserveInputButHideProviderDetails(t *testing.T) {
	item := CallLogItem{
		RequestParams: `{"target":"https://upstream.example/internal?url=https%3A%2F%2Fprivate.example/item"}`,
		RequestValue:  "https://upstream.example/internal?token=secret",
		ResponseBody:  `{"next":"https://backup.example/health"}`,
		UpstreamURL:   "https://upstream.example/internal",
	}
	sanitizeCallLogEndpointFields(&item)
	if !strings.Contains(item.RequestParams, "upstream.example/internal") || !strings.Contains(item.RequestValue, "upstream.example/internal") || strings.Contains(item.RequestValue, "token=secret") {
		t.Fatalf("caller input was erased or credentials leaked: %+v", item)
	}
	// UpstreamURL is server-side routing configuration and keeps the strict
	// endpoint redaction.
	for _, forbidden := range []string{"upstream.example", "private.example", "/internal", "https://"} {
		if strings.Contains(item.UpstreamURL, forbidden) {
			t.Fatalf("unmasked call-log field leaked endpoint detail %q: %s", forbidden, item.UpstreamURL)
		}
	}
	// ResponseBody is what the caller already received: a public business URL
	// must stay readable or the log cannot be used for troubleshooting.
	if !strings.Contains(item.ResponseBody, "https://backup.example/health") {
		t.Fatalf("business response URL was erased: %s", item.ResponseBody)
	}
}

func TestUnmaskedCallLogFieldsHideEncodedAndPrivateEndpointForms(t *testing.T) {
	item := CallLogItem{
		RequestParams: `{"target":"https\u003a\u002f\u002fsecret.example\u002fprivate\u003ftoken\u003dsecret"}`,
		RequestValue:  "dial tcp api-backend:9443/v1/private: i/o timeout",
		ResponseBody:  `{"next":"https%253A%252F%252Fnested.example%252Fhealth","internal":"http://10.0.0.8:8080/private?token=secret"}`,
		ErrorMessage:  "dial tcp [2001:db8::1]:443/private: i/o timeout",
		UpstreamURL:   "https://secret.example/private",
	}
	sanitizeCallLogEndpointFields(&item)
	if !strings.Contains(item.RequestParams, "secret.example/private") || strings.Contains(item.RequestParams, "token=secret") || !json.Valid([]byte(item.RequestParams)) {
		t.Fatalf("encoded caller URL was erased or credential leaked: %s", item.RequestParams)
	}
	for label, value := range map[string]string{
		"requestValue": item.RequestValue,
		"errorMessage": item.ErrorMessage,
		"upstreamURL":  item.UpstreamURL,
	} {
		for _, forbidden := range []string{
			"secret.example", "nested.example", "api-backend", "2001:db8::1", "9443", "443",
			"/private", "/health", "token", "secret", "%252F", "\\u003a", "\\u002f",
		} {
			if strings.Contains(value, forbidden) {
				t.Fatalf("%s leaked %q: %s", label, forbidden, value)
			}
		}
		if !strings.Contains(value, "[UPSTREAM_REDACTED]") {
			t.Fatalf("%s lost endpoint marker: %s", label, value)
		}
	}
	// ResponseBody keeps reachable business URLs but internal hosts and
	// credentials inside them must still disappear.
	for _, forbidden := range []string{"10.0.0.8", "8080", "token=", "secret"} {
		if strings.Contains(item.ResponseBody, forbidden) {
			t.Fatalf("response body leaked %q: %s", forbidden, item.ResponseBody)
		}
	}
	if !strings.Contains(item.ResponseBody, "[INTERNAL_REDACTED]") {
		t.Fatalf("internal endpoint in the response was not marked: %s", item.ResponseBody)
	}
}

func TestToUserCallLogItemHidesInternalFields(t *testing.T) {
	got := toUserCallLogItem(CallLogItem{
		APIKeyID:        "42",
		BillingSnapshot: `{"apiId":1,"price":"0.01"}`,
		ClientIP:        "10.0.1.24",
		InterfaceID:     "99",
		RequestParams:   `{"query":{"email":["alice@example.com"]},"body":{"password":"secret-password","keyword":"ok","upstreamUrl":"http://10.0.0.8:8080/private"}}`,
		RequestValue:    "https://client.example.com/path?token=secret&mobile=13800138000",
		ResponseBody:    `{"refreshToken":"refresh-secret","data":{"value":"ok","serverAddress":"api.internal:9000"}}`,
		TraceID:         "internal-trace-id",
		UpstreamURL:     "https://upstream.example.com/path?api_key=secret",
		ErrorMessage:    "upstream password=secret-password token=raw-token",
	})
	if strings.Contains(got.ErrorMessage, "secret-password") || strings.Contains(got.ErrorMessage, "raw-token") {
		t.Fatalf("user error message not masked: %s", got.ErrorMessage)
	}
	if strings.Contains(got.RequestParams, "alice@example.com") || strings.Contains(got.RequestParams, "secret-password") || strings.Contains(got.RequestParams, "upstreamUrl") {
		t.Fatalf("user request params not masked: %s", got.RequestParams)
	}
	if strings.Contains(got.ResponseBody, "refresh-secret") || strings.Contains(got.ResponseBody, "serverAddress") {
		t.Fatalf("user response body not masked: %s", got.ResponseBody)
	}

	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal user call log: %v", err)
	}
	for _, forbidden := range []string{
		"apiKeyId", "billingSnapshot", "clientIp", "clientRegion", "interfaceId",
		"requestValue", "upstreamUrl", "userId", "username",
	} {
		if strings.Contains(string(encoded), `"`+forbidden+`"`) {
			t.Fatalf("user call-log payload exposed forbidden field %q: %s", forbidden, encoded)
		}
	}
}

func TestSanitizeUserErrorMessageHidesUpstreamNetworkDetails(t *testing.T) {
	unsafe := []string{
		`Get "https://private-upstream.example/internal": context deadline exceeded`,
		"dial tcp 10.20.30.40:8080: connectex: connection refused",
		"upstream 192.168.1.20:9000 failed",
		"TLS handshake timeout",
	}
	for _, value := range unsafe {
		if got := sanitizeUserErrorMessage(value); got != "上游服务暂时不可用" {
			t.Fatalf("network detail was not hidden: %q -> %q", value, got)
		}
	}
	if got := sanitizeUserErrorMessage("缺少必填参数 city"); got != "缺少必填参数 city" {
		t.Fatalf("business validation detail was lost: %q", got)
	}
}

func TestDashboardOpenTrafficConditionExcludesAdminTestLogs(t *testing.T) {
	got := dashboardOpenTrafficCondition("l")
	if !strings.Contains(got, "l.charge_type") {
		t.Fatalf("expected aliased charge_type predicate, got %q", got)
	}
	if !strings.Contains(got, "<> 'ADMIN'") {
		t.Fatalf("expected ADMIN management-test logs to be excluded, got %q", got)
	}
	if strings.Contains(strings.ToLower(got), "coalesce(") {
		t.Fatalf("open traffic predicate should keep charge_type indexable, got %q", got)
	}
	if strings.ContainsAny(got, ";?") {
		t.Fatalf("dashboard open traffic predicate should stay a static SQL fragment, got %q", got)
	}

	plain := dashboardOpenTrafficCondition("")
	if strings.Contains(plain, "l.charge_type") || !strings.Contains(plain, "charge_type") {
		t.Fatalf("unexpected unaliased predicate: %q", plain)
	}
}
