package ipregion

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"bugpk-api/internal/config"
)

func TestGeoIPResultIncludesCoordinatesAndSafeStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"country":"中国","region":"广东","city":"深圳","latitude":22.5431,"longitude":114.0579,"connection":{"isp":"Example ISP"}}`))
	}))
	defer server.Close()

	resolver := NewResolver(config.IPRegionConfig{Enabled: true, Provider: "ip-api", Timeout: time.Second}, nil)
	resolver.client = server.Client()
	resolver.cfg.IPApiEndpoint = ""
	resolver.fallback.Enabled = false
	// 用本地端点替换默认源，同时保留默认字段映射。
	resolver.SetSourceLoader(func(context.Context) (SourceConfig, error) {
		return SourceConfig{
			Name:      "test-source",
			APIURL:    server.URL + "/{ip}",
			ParseRule: DefaultSourceConfig().ParseRule,
			Enabled:   true,
		}, nil
	})

	result := NewHandler(resolver).geoIPResult(context.Background(), "8.8.8.8")
	if !result.Resolved || result.Status != "resolved" || result.Region != "中国 广东 深圳" {
		t.Fatalf("unexpected geoip result: %#v", result)
	}
	if result.Latitude == nil || result.Longitude == nil || *result.Latitude != 22.5431 || *result.Longitude != 114.0579 {
		t.Fatalf("coordinates missing from result: %#v", result)
	}
	if result.Provider == "" || result.ISP != "Example ISP" {
		t.Fatalf("provider/isp missing from result: %#v", result)
	}
}

func TestGeoIPResultClassifiesInvalidPrivateAndUnavailable(t *testing.T) {
	handler := NewHandler(nil)
	if result := handler.geoIPResult(context.Background(), "not-an-ip"); result.Status != "invalid" || result.Resolved {
		t.Fatalf("invalid IP result = %#v", result)
	}
	if result := handler.geoIPResult(context.Background(), "127.0.0.1"); result.Status != "private" || result.Resolved {
		t.Fatalf("private IP result = %#v", result)
	}
	if result := handler.geoIPResult(context.Background(), "8.8.8.8"); result.Status != "unavailable" || result.Resolved {
		t.Fatalf("unavailable resolver result = %#v", result)
	}
}

func TestGeoIPResultDoesNotExposeProviderFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":"internal-provider.example/private/path"}`))
	}))
	defer server.Close()

	resolver := NewResolver(config.IPRegionConfig{
		Enabled:       true,
		Provider:      "ip-api",
		Timeout:       time.Second,
		IPApiEndpoint: server.URL + "/{ip}",
	}, nil)
	resolver.client = server.Client()
	resolver.fallback.Enabled = false

	result := NewHandler(resolver).geoIPResult(context.Background(), "8.8.8.8")
	if result.Status != "unavailable" || result.Resolved || result.Provider != "" || result.Region != "" {
		t.Fatalf("provider failure leaked details: %#v", result)
	}
}
