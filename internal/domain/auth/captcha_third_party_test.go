package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	apperrors "bugpk-api/internal/pkg/errors"

	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

func TestGeeTest4ProviderVerifyPostsSignedOfficialPayload(t *testing.T) {
	var received url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if got := r.Header.Get("Content-Type"); got != "application/x-www-form-urlencoded" {
			t.Errorf("content type = %q", got)
		}
		if err := r.ParseForm(); err != nil {
			t.Errorf("ParseForm() error = %v", err)
		}
		received = cloneURLValues(r.PostForm)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":"success","reason":""}`))
	}))
	defer server.Close()

	provider := NewGeeTest4ProviderWithHTTPClient(server.Client(), server.URL)
	cfg := ProviderConfig{SiteKey: "captcha-id", SecretRef: "api-key", TimeoutMS: 1000}
	ok, err := provider.Verify(context.Background(), StoredChallenge{
		CaptchaID: "internal-id", Provider: ProviderGeeTest4, Scene: "login",
	}, cfg, ChallengeSolution{
		LotNumber: "lot-number", CaptchaOutput: "captcha-output",
		PassToken: "pass-token", GenTime: "1700000000",
	})
	if err != nil || !ok {
		t.Fatalf("Verify() = (%v, %v), want success", ok, err)
	}
	want := map[string]string{
		"captcha_id":     "captcha-id",
		"lot_number":     "lot-number",
		"captcha_output": "captcha-output",
		"pass_token":     "pass-token",
		"gen_time":       "1700000000",
		"sign_token":     expectedHMACSHA256("api-key", "lot-number"),
	}
	for key, value := range want {
		if received.Get(key) != value {
			t.Errorf("form[%q] = %q, want %q", key, received.Get(key), value)
		}
	}
}

func TestGeeTest4ProviderVerifyRejectsFailedValidation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"result":"fail","reason":"risk"}`))
	}))
	defer server.Close()
	provider := NewGeeTest4ProviderWithHTTPClient(server.Client(), server.URL)
	ok, err := provider.Verify(context.Background(), StoredChallenge{Provider: ProviderGeeTest4}, ProviderConfig{
		SiteKey: "captcha-id", SecretRef: "api-key", TimeoutMS: 1000,
	}, validGeeTestSolution())
	if ok {
		t.Fatal("failed GeeTest validation unexpectedly passed")
	}
	assertBusinessStatus(t, err, consts.StatusBadRequest)
}

func TestGeeTest4ProviderVerifyMapsUpstreamFailuresToUnavailable(t *testing.T) {
	tests := []struct {
		name      string
		handler   http.HandlerFunc
		timeoutMS int
	}{
		{
			name: "http error",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "upstream failure", http.StatusBadGateway)
			},
			timeoutMS: 1000,
		},
		{
			name: "ambiguous json",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`{"result":"success","result":"fail"}`))
			},
			timeoutMS: 1000,
		},
		{
			name: "oversized body",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(strings.Repeat("x", int(maxCaptchaProviderBody)+1)))
			},
			timeoutMS: 1000,
		},
		{
			name: "timeout",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				time.Sleep(200 * time.Millisecond)
				_, _ = w.Write([]byte(`{"result":"success"}`))
			},
			timeoutMS: 100,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(test.handler)
			defer server.Close()
			provider := NewGeeTest4ProviderWithHTTPClient(server.Client(), server.URL)
			ok, err := provider.Verify(context.Background(), StoredChallenge{Provider: ProviderGeeTest4}, ProviderConfig{
				SiteKey: "captcha-id", SecretRef: "api-key", TimeoutMS: test.timeoutMS,
			}, validGeeTestSolution())
			if ok {
				t.Fatal("upstream failure unexpectedly passed")
			}
			assertBusinessStatus(t, err, consts.StatusServiceUnavailable)
		})
	}
}

func TestCloudflareProviderVerifyPostsBoundPayload(t *testing.T) {
	var received url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("ParseForm() error = %v", err)
		}
		received = cloneURLValues(r.PostForm)
		_, _ = w.Write([]byte(`{"success":true,"action":"login_action","hostname":"api.example.com","cdata":"bugpk_internal123","error-codes":[]}`))
	}))
	defer server.Close()

	provider := NewCloudflareTurnstileProviderWithHTTPClient(server.Client(), server.URL)
	cfg := ProviderConfig{
		SiteKey: "site-key", SecretRef: "secret-key", TimeoutMS: 1000,
		Extra: map[string]any{
			"action":   "login_action",
			"cdata":    "bugpk",
			"hostname": "API.EXAMPLE.COM",
		},
	}
	ok, err := provider.Verify(context.Background(), StoredChallenge{
		CaptchaID: "internal123", Provider: ProviderCloudflare, Scene: "login",
	}, cfg, ChallengeSolution{Token: "turnstile-token", RemoteIP: "203.0.113.10"})
	if err != nil || !ok {
		t.Fatalf("Verify() = (%v, %v), want success", ok, err)
	}
	for key, value := range map[string]string{
		"secret":   "secret-key",
		"response": "turnstile-token",
		"remoteip": "203.0.113.10",
	} {
		if received.Get(key) != value {
			t.Errorf("form[%q] = %q, want %q", key, received.Get(key), value)
		}
	}
}

func TestCloudflareProviderVerifyRejectsResponseBindingMismatch(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "action", body: `{"success":true,"action":"register","hostname":"api.example.com","cdata":"internal123"}`},
		{name: "hostname", body: `{"success":true,"action":"login","hostname":"evil.example","cdata":"internal123"}`},
		{name: "cdata", body: `{"success":true,"action":"login","hostname":"api.example.com","cdata":"other"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			provider := NewCloudflareTurnstileProviderWithHTTPClient(server.Client(), server.URL)
			ok, err := provider.Verify(context.Background(), StoredChallenge{
				CaptchaID: "internal123", Provider: ProviderCloudflare, Scene: "login",
			}, ProviderConfig{
				SiteKey: "site-key", SecretRef: "secret-key", TimeoutMS: 1000,
				Extra: map[string]any{"hostname": "api.example.com"},
			}, ChallengeSolution{Token: "token"})
			if ok {
				t.Fatal("binding mismatch unexpectedly passed")
			}
			assertBusinessStatus(t, err, consts.StatusBadRequest)
		})
	}
}

func TestCloudflareProviderVerifyClassifiesProviderAndClientFailures(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantStatus int
	}{
		{name: "invalid token", body: `{"success":false,"error-codes":["invalid-input-response"]}`, wantStatus: consts.StatusBadRequest},
		{name: "provider internal error", body: `{"success":false,"error-codes":["internal-error"]}`, wantStatus: consts.StatusServiceUnavailable},
		{name: "invalid server secret", body: `{"success":false,"error-codes":["invalid-input-secret"]}`, wantStatus: consts.StatusServiceUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			provider := NewCloudflareTurnstileProviderWithHTTPClient(server.Client(), server.URL)
			ok, err := provider.Verify(context.Background(), StoredChallenge{
				CaptchaID: "internal123", Provider: ProviderCloudflare, Scene: "login",
			}, ProviderConfig{SiteKey: "site-key", SecretRef: "secret-key", TimeoutMS: 1000}, ChallengeSolution{Token: "token"})
			if ok {
				t.Fatal("failed validation unexpectedly passed")
			}
			assertBusinessStatus(t, err, test.wantStatus)
		})
	}
}

func TestCloudflareProviderRejectsOversizedTokenBeforeNetwork(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer server.Close()
	provider := NewCloudflareTurnstileProviderWithHTTPClient(server.Client(), server.URL)
	ok, err := provider.Verify(context.Background(), StoredChallenge{
		CaptchaID: "internal123", Provider: ProviderCloudflare, Scene: "login",
	}, ProviderConfig{SiteKey: "site-key", SecretRef: "secret-key"}, ChallengeSolution{
		Token: strings.Repeat("x", maxTurnstileTokenBytes+1),
	})
	if ok || calls.Load() != 0 {
		t.Fatalf("oversized token result ok=%v calls=%d", ok, calls.Load())
	}
	assertBusinessStatus(t, err, consts.StatusBadRequest)
}

func TestCloudflareProviderIssueRejectsInvalidSecurityBindings(t *testing.T) {
	tests := []map[string]any{
		{"action": "contains spaces"},
		{"cdata": "contains/slash"},
		{"hostname": "https://example.com"},
	}
	provider := NewCloudflareTurnstileProvider()
	for _, extra := range tests {
		_, err := provider.Issue(context.Background(), "login", ProviderConfig{
			SiteKey: "site-key", SecretRef: "secret-key", Extra: extra,
		}, "")
		assertBusinessStatus(t, err, consts.StatusServiceUnavailable)
	}
}

func validGeeTestSolution() ChallengeSolution {
	return ChallengeSolution{
		LotNumber: "lot-number", CaptchaOutput: "captcha-output",
		PassToken: "pass-token", GenTime: "1700000000",
	}
}

func expectedHMACSHA256(key, value string) string {
	mac := hmac.New(sha256.New, []byte(key))
	_, _ = fmt.Fprint(mac, value)
	return hex.EncodeToString(mac.Sum(nil))
}

func cloneURLValues(values url.Values) url.Values {
	result := make(url.Values, len(values))
	for key, entries := range values {
		result[key] = append([]string(nil), entries...)
	}
	return result
}

func assertBusinessStatus(t *testing.T, err error, want int) {
	t.Helper()
	business, ok := apperrors.AsBusinessError(err)
	if !ok || business.StatusCode != want {
		t.Fatalf("error = %v, want business status %d", err, want)
	}
}
