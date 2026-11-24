package masking

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestEmail(t *testing.T) {
	got := Email("alice@example.com")
	if got != "al***e@example.com" {
		t.Fatalf("Email() = %q", got)
	}
}

func TestMobile(t *testing.T) {
	got := Mobile("13800138000")
	if got != "138****8000" {
		t.Fatalf("Mobile() = %q", got)
	}
}

func TestIP(t *testing.T) {
	got := IP("192.168.1.100")
	if got != "192.168.*.*" {
		t.Fatalf("IP() = %q", got)
	}
}

func TestSensitiveJSONText(t *testing.T) {
	input := `{"api_key":"bp_live_secret","profile":{"email":"alice@example.com","mobile":"13800138000"},"items":[{"accessToken":"abc"}],"normal":"ok"}`
	got := SensitiveJSONText(input)
	var parsed map[string]any
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("SensitiveJSONText returned invalid JSON: %v", err)
	}
	if parsed["api_key"] != redactedValue {
		t.Fatalf("api_key not redacted: %s", got)
	}
	profile := parsed["profile"].(map[string]any)
	if profile["email"] != "al***e@example.com" {
		t.Fatalf("email not masked: %s", got)
	}
	if profile["mobile"] != "138****8000" {
		t.Fatalf("mobile not masked: %s", got)
	}
	items := parsed["items"].([]any)
	item := items[0].(map[string]any)
	if item["accessToken"] != redactedValue {
		t.Fatalf("accessToken not redacted: %s", got)
	}
	if parsed["normal"] != "ok" {
		t.Fatalf("normal field changed: %s", got)
	}
}

func TestSensitiveJSONTextLeavesNonJSON(t *testing.T) {
	got := SensitiveJSONText("plain secret string")
	if got != "plain secret string" {
		t.Fatalf("non-json value changed: %q", got)
	}
}

func TestSensitiveJSONTextPreservesLargeAndPreciseNumbers(t *testing.T) {
	input := `{"largeId":9007199254740993,"amount":1234567890.123456789,"token":"secret"}`
	got := SensitiveJSONText(input)
	if !strings.Contains(got, `"largeId":9007199254740993`) {
		t.Fatalf("large integer lost precision: %s", got)
	}
	if !strings.Contains(got, `"amount":1234567890.123456789`) {
		t.Fatalf("decimal lost precision: %s", got)
	}
	if strings.Contains(got, `"token":"secret"`) {
		t.Fatalf("sensitive field was not redacted: %s", got)
	}
}

func TestSensitiveJSONTextRedactsExtendedCredentialKeys(t *testing.T) {
	input := `{"privateKey":"private","clientSecret":"client","idToken":"id","resetToken":"reset","csrf_token":"csrf","sessionId":"session","webhookSecret":"webhook","authCode":"auth-code","normal":"ok"}`
	got := SensitiveJSONText(input)
	var parsed map[string]any
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("masked extended credentials are invalid JSON: %v", err)
	}
	for _, key := range []string{"privateKey", "clientSecret", "idToken", "resetToken", "csrf_token", "sessionId", "webhookSecret", "authCode"} {
		if parsed[key] != redactedValue {
			t.Fatalf("extended credential %q was not redacted: %s", key, got)
		}
	}
	if parsed["normal"] != "ok" {
		t.Fatalf("normal field changed: %s", got)
	}
}

func TestSensitiveJSONTextRejectsTrailingJSONContent(t *testing.T) {
	input := `{"token":"secret"} {"other":true}`
	if got := SensitiveJSONText(input); got != input {
		t.Fatalf("invalid trailing JSON was partially accepted: %q", got)
	}
}

func TestUserVisibleJSONTextRemovesInternalRoutingAndNetworkData(t *testing.T) {
	input := `{"data":{"value":"ok","upstreamUrl":"http://10.0.0.8:8080/private","internalRoute":"/admin/proxy","nested":{"serverAddress":"api.internal:9000","publicUrl":"https://example.com/result?token=secret"}},"message":"backend 192.168.1.9 failed"}`
	got := UserVisibleJSONText(input)
	var parsed map[string]any
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("UserVisibleJSONText returned invalid JSON: %v", err)
	}
	for _, forbidden := range []string{"upstreamUrl", "internalRoute", "serverAddress", "10.0.0.8", "192.168.1.9", "api.internal"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("user-visible JSON leaked %q: %s", forbidden, got)
		}
	}
	if !strings.Contains(got, `"value":"ok"`) || !strings.Contains(got, "https://example.com/result?token=%5BREDACTED%5D") {
		t.Fatalf("public business data was not preserved: %s", got)
	}
}

func TestInternalNetworkTextPreservesPublicAddresses(t *testing.T) {
	input := "public https://8.8.8.8/dns and https://example.com/path, private http://127.0.0.1:8080/admin and fd00::1"
	got := InternalNetworkText(input)
	if !strings.Contains(got, "https://8.8.8.8/dns") || !strings.Contains(got, "https://example.com/path") {
		t.Fatalf("public addresses were unexpectedly removed: %s", got)
	}
	for _, forbidden := range []string{"127.0.0.1", "fd00::1"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("private address %q leaked: %s", forbidden, got)
		}
	}
}

func TestURLString(t *testing.T) {
	got := URLString("https://example.com/api?key=secret&email=alice%40example.com&normal=ok")
	if !strings.Contains(got, "key=%5BREDACTED%5D") {
		t.Fatalf("key not redacted: %s", got)
	}
	if !strings.Contains(got, "email=al%2A%2A%2Ae%40example.com") {
		t.Fatalf("email not masked: %s", got)
	}
	if !strings.Contains(got, "normal=ok") {
		t.Fatalf("normal query changed: %s", got)
	}
}

func TestURLStringRedactsUserInfoWithoutQuery(t *testing.T) {
	got := URLString("https://api-user:super-secret@example.com/path")
	if strings.Contains(got, "api-user") || strings.Contains(got, "super-secret") || !strings.Contains(got, "%5BREDACTED%5D@example.com") {
		t.Fatalf("URL userinfo was not redacted: %s", got)
	}
}

func TestErrorTextMasksCommonCredentialShapes(t *testing.T) {
	input := strings.Join([]string{
		"connect bugpk:p@ss:w/rd?#@tcp([2001:db8::10]:3306)/bugpk",
		"password=plain-secret",
		"Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.payload.signature",
		"api_key=bp_live_very_secret",
		"Cookie: session=raw-session; refresh_token=raw-refresh",
		"email=alice@example.com mobile=13800138000",
		"url=https://api-user:url-password@example.com/path?token=url-secret",
	}, "\n")

	got := ErrorText(input)
	for _, secret := range []string{
		"p@ss:w/rd?#",
		"plain-secret",
		"eyJhbGciOiJIUzI1NiJ9.payload.signature",
		"bp_live_very_secret",
		"raw-session",
		"raw-refresh",
		"alice@example.com",
		"13800138000",
		"api-user",
		"url-password",
		"url-secret",
	} {
		if strings.Contains(got, secret) {
			t.Fatalf("ErrorText leaked %q in %q", secret, got)
		}
	}
	if !strings.Contains(got, redactedValue) || !strings.Contains(got, "al***e@example.com") || !strings.Contains(got, "138****8000") {
		t.Fatalf("ErrorText lost expected masked diagnostics: %q", got)
	}
}

func TestErrorTextMasksExtendedCredentialShapes(t *testing.T) {
	input := "client_secret=client-secret id_token=id-secret session_token=session-secret webhook_secret=webhook-secret signing_key=signing-secret"
	got := ErrorText(input)
	for _, secret := range []string{"client-secret", "id-secret", "session-secret", "webhook-secret", "signing-secret"} {
		if strings.Contains(got, secret) {
			t.Fatalf("extended plain-text credential leaked %q: %s", secret, got)
		}
	}
}

func TestErrorTextRedactsPEMPrivateKey(t *testing.T) {
	input := "parse key: -----BEGIN PRIVATE KEY-----\nvery-secret-key-material\n-----END PRIVATE KEY-----"
	got := ErrorText(input)
	if strings.Contains(got, "very-secret-key-material") || !strings.Contains(got, redactedValue) {
		t.Fatalf("private key was not redacted: %q", got)
	}
}

func TestErrorTextRedactsCompleteMySQLDSN(t *testing.T) {
	t.Parallel()
	input := `database probe failed: root:super-secret@tcp(db.internal.example:3306/customer?parseTime=true)`
	for label, got := range map[string]string{
		"error": ErrorText(input),
		"stack": StackText(input),
	} {
		for _, forbidden := range []string{"root", "super-secret", "db.internal.example", "3306", "customer", "parseTime"} {
			if strings.Contains(got, forbidden) {
				t.Fatalf("%s leaked MySQL DSN detail %q: %s", label, forbidden, got)
			}
		}
		if !strings.Contains(got, "[UPSTREAM_REDACTED]") {
			t.Fatalf("%s lost DSN redaction marker: %s", label, got)
		}
	}
}

func TestErrorTextMasksQuotedValuesWithSpaces(t *testing.T) {
	input := `{"password":"long secret phrase","secret":'single quoted secret value',"token":"token with spaces"}`
	got := ErrorText(input)
	for _, secret := range []string{"long secret phrase", "single quoted secret value", "token with spaces"} {
		if strings.Contains(got, secret) {
			t.Fatalf("quoted credential leaked %q in %q", secret, got)
		}
	}
	if strings.Count(got, redactedValue) < 3 {
		t.Fatalf("expected all quoted credentials to be masked: %q", got)
	}
}

func TestErrorTextRedactsUpstreamURLsIncludingEncodedNestedURLs(t *testing.T) {
	input := `Get "https://parse.upstream.example.com/api/parse?url=https%3A%2F%2Fv.kuaishou.com%2FAbCdEf12": net/http: timeout awaiting response headers`
	got := ErrorText(input)
	for _, forbidden := range []string{
		"parse.upstream.example.com",
		"v.kuaishou.com",
		"AbCdEf12",
		"https%3A%2F%2Fv.kuaishou.com",
		"/api/parse",
	} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("ErrorText leaked upstream detail %q: %s", forbidden, got)
		}
	}
	if !strings.Contains(got, "timeout awaiting response headers") || !strings.Contains(got, "[UPSTREAM_REDACTED]") {
		t.Fatalf("ErrorText discarded safe timeout context or marker: %s", got)
	}
}

func TestLogTextDoesNotDuplicateEndpointMarkerAfterURLCredentialMasking(t *testing.T) {
	got := LogText("https://example.com/api?token=secret&mobile=13800138000")
	if strings.Contains(got, "][") || strings.Contains(got, "]]") {
		t.Fatalf("LogText produced malformed endpoint marker: %q", got)
	}
	if strings.Contains(got, "example.com") || strings.Contains(got, "/api") {
		t.Fatalf("LogText leaked endpoint details: %q", got)
	}
}

func TestUpstreamReferenceKeepsOnlySyntheticNodeLabels(t *testing.T) {
	if got := UpstreamReference("https://primary.example/private?token=secret"); got != "[UPSTREAM_REDACTED]" {
		t.Fatalf("UpstreamReference() = %q", got)
	}
	if got := UpstreamReference("node-1,node-2"); got != "node-1,node-2" {
		t.Fatalf("safe node references were not preserved: %q", got)
	}
	if got := UpstreamReferences([]string{"https://primary.example", "", "https://backup.example"}); got != "node-1,node-2" {
		t.Fatalf("UpstreamReferences() = %q", got)
	}
}

func TestErrorTextRedactsStandalonePercentEncodedURL(t *testing.T) {
	input := `upstream request failed: url=https%3A%2F%2Fparse.upstream.example.com%2Fapi%2Fparse%3Furl%3Dhttps%253A%252F%252Fv.kuaishou.com%252FAbCdEf12`
	got := ErrorText(input)
	for _, forbidden := range []string{"parse.upstream.example.com", "v.kuaishou.com", "AbCdEf12", "%2Fapi%2Fparse"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("ErrorText leaked encoded upstream detail %q: %s", forbidden, got)
		}
	}
}

func TestErrorTextRedactsUpstreamIPEndpoint(t *testing.T) {
	input := `dial tcp 203.0.113.10:8443/api/parse: i/o timeout`
	got := ErrorText(input)
	if strings.Contains(got, "203.0.113.10") || strings.Contains(got, ":8443/api/parse") {
		t.Fatalf("ErrorText leaked upstream IP endpoint: %s", got)
	}
	if !strings.Contains(got, "i/o timeout") {
		t.Fatalf("ErrorText discarded timeout context: %s", got)
	}
}

func TestErrorTextRedactsBracketedUpstreamIPv6Endpoint(t *testing.T) {
	input := `dial tcp [2001:db8::1]:443: i/o timeout`
	got := ErrorText(input)
	if strings.Contains(got, "2001:db8::1") || strings.Contains(got, ":443") {
		t.Fatalf("ErrorText leaked upstream IPv6 endpoint: %s", got)
	}
	if !strings.Contains(got, "i/o timeout") || !strings.Contains(got, "[UPSTREAM_REDACTED]") {
		t.Fatalf("ErrorText discarded safe IPv6 timeout context: %s", got)
	}
}

func TestStackTextRedactsExplicitEndpointsWithoutDamagingSourceFrames(t *testing.T) {
	input := "panic in recovery_test.go: Get https://parse.upstream.example.com/api/parse?token=secret: timeout"
	got := StackText(input)
	for _, forbidden := range []string{"parse.upstream.example.com", "/api/parse", "token=secret"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("stack text leaked %q: %s", forbidden, got)
		}
	}
	if !strings.Contains(got, "recovery_test.go") || !strings.Contains(got, "timeout") {
		t.Fatalf("stack source context was damaged: %s", got)
	}
}

func TestStackTextDoesNotDuplicateEndpointMarkerAfterCredentialMasking(t *testing.T) {
	got := StackText(`Get "https://example.com/api?token=secret": timeout`)
	if strings.Contains(got, "]]") {
		t.Fatalf("StackText produced malformed endpoint marker: %q", got)
	}
	if strings.Contains(got, "example.com") || strings.Contains(got, "/api") {
		t.Fatalf("StackText leaked endpoint details: %q", got)
	}
}

func TestStackTextRedactsBareDNSEndpointInNetworkDiagnostic(t *testing.T) {
	for _, input := range []string{
		"lookup parse.upstream.example.com: no such host",
		"lookup example.com: no such host",
		"dial tcp: lookup api-backend on 127.0.0.11:53: no such host",
		"dial tcp parse.upstream.example.com:8443/api/parse: i/o timeout",
	} {
		got := StackText(input)
		for _, forbidden := range []string{"parse.upstream.example.com", "api-backend", ":8443", "/api/parse"} {
			if strings.Contains(got, forbidden) {
				t.Fatalf("StackText leaked bare DNS endpoint detail %q: %s", forbidden, got)
			}
		}
		if !strings.Contains(got, "[UPSTREAM_REDACTED]") {
			t.Fatalf("StackText lost endpoint marker: %s", got)
		}
	}
	if got := StackText("recovery_test.go:42"); !strings.Contains(got, "recovery_test.go:42") {
		t.Fatalf("StackText damaged source frame: %s", got)
	}
}

func TestTextMasksURLOrJSON(t *testing.T) {
	urlValue := Text("https://example.com/api?key=secret&phone=13800138000")
	if !strings.Contains(urlValue, "key=%5BREDACTED%5D") {
		t.Fatalf("url key not redacted: %s", urlValue)
	}
	if !strings.Contains(urlValue, "phone=138%2A%2A%2A%2A8000") {
		t.Fatalf("url phone not masked: %s", urlValue)
	}

	jsonValue := Text(`{"secretKey":"secret","email":"alice@example.com"}`)
	if !strings.Contains(jsonValue, `"secretKey":"[REDACTED]"`) {
		t.Fatalf("json secretKey not redacted: %s", jsonValue)
	}
	if !strings.Contains(jsonValue, `"email":"al***e@example.com"`) {
		t.Fatalf("json email not masked: %s", jsonValue)
	}
}

func TestEndpointRedactionHandlesEncodedAndPrivateTransportForms(t *testing.T) {
	tests := []struct {
		name  string
		input string
		bad   []string
	}{
		{
			name:  "json unicode escaped URL",
			input: `{"url":"https\u003a\u002f\u002fsecret.example\u002fprivate\u003ftoken\u003dsecret"}`,
			bad:   []string{"secret.example", "private", "token", "secret", "\\u003a", "\\u002f"},
		},
		{
			name:  "repeated percent encoded URL",
			input: `upstream=https%253A%252F%252Fsecret.example%252Fprivate%253Furl%253Dhttps%25253A%25252F%25252Fnested.example%25252Fitem`,
			bad:   []string{"secret.example", "nested.example", "private", "item", "%252F"},
		},
		{
			name:  "localhost transport endpoint",
			input: "dial tcp localhost:8080/api/private: i/o timeout",
			bad:   []string{"localhost", "8080", "/api/private"},
		},
		{
			name:  "single label transport endpoint",
			input: "dial tcp api-backend:9443/v1/private: i/o timeout",
			bad:   []string{"api-backend", "9443", "/v1/private"},
		},
		{
			name:  "bare DNS transport endpoint",
			input: "lookup api-backend: no such host",
			bad:   []string{"api-backend"},
		},
		{
			name:  "resolver-qualified single label endpoint",
			input: "dial tcp: lookup api-backend on 127.0.0.11:53: no such host",
			bad:   []string{"api-backend", "127.0.0.11", ":53"},
		},
		{
			name:  "bare localhost DNS endpoint",
			input: "lookup localhost: no such host",
			bad:   []string{"localhost"},
		},
		{
			name:  "unbracketed IPv6 transport endpoint",
			input: "dial tcp 2001:db8::1:443/private: i/o timeout",
			bad:   []string{"2001:db8::1", "443", "/private"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			for label, got := range map[string]string{
				"endpoint": EndpointText(test.input),
				"error":    ErrorText(test.input),
				"stack":    StackText(test.input),
			} {
				for _, forbidden := range test.bad {
					if strings.Contains(got, forbidden) {
						t.Fatalf("%s leaked %q: %s", label, forbidden, got)
					}
				}
				if !strings.Contains(got, "[UPSTREAM_REDACTED]") {
					t.Fatalf("%s lost endpoint marker: %s", label, got)
				}
				if strings.Contains(test.input, "timeout") && !strings.Contains(got, "timeout") {
					t.Fatalf("%s discarded safe timeout context: %s", label, got)
				}
			}
		})
	}
	if got := StackText("panic in recovery_test.go:42"); !strings.Contains(got, "recovery_test.go:42") {
		t.Fatalf("StackText damaged source frame: %s", got)
	}
}

func TestErrorTextRedactsGenericTransportURLsAndCredentials(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
		bad   []string
	}{
		{
			name:  "mysql URI",
			input: `dial mysql://root:secret@db.internal:3306/app?token=hidden: i/o timeout`,
			bad:   []string{"mysql://", "root", "secret", "db.internal", "3306", "/app", "token=hidden"},
		},
		{
			name:  "redis URI",
			input: `dial redis://:pass@10.0.0.1:6379/0: connection refused`,
			bad:   []string{"redis://", ":pass", "10.0.0.1", "6379", "/0"},
		},
		{
			name:  "grpc URI",
			input: `request grpc://svc:8443/internal.Service/Call failed`,
			bad:   []string{"grpc://", "svc", "8443", "/internal.Service/Call"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			for label, got := range map[string]string{
				"error":    ErrorText(test.input),
				"endpoint": EndpointText(test.input),
				"stack":    StackText(test.input),
			} {
				for _, forbidden := range test.bad {
					if strings.Contains(got, forbidden) {
						t.Fatalf("%s leaked generic transport detail %q: %s", label, forbidden, got)
					}
				}
				if !strings.Contains(got, "[UPSTREAM_REDACTED]") {
					t.Fatalf("%s lost endpoint marker: %s", label, got)
				}
			}
		})
	}
}
