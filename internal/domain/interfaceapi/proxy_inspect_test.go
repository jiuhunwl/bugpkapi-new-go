package interfaceapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"bugpk-api/internal/pkg/httpclient"
	"bugpk-api/internal/pkg/proxypool"
)

func TestInspectProxyPoolConfigReturnsDetectedPathAndCandidates(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("X-Pool-Token") != "pool-token" {
			t.Fatalf("X-Pool-Token = %q", request.Header.Get("X-Pool-Token"))
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"data":{"items":[{"proxy_ip":"203.0.113.20","proxy_port":3128}]}}`))
	}))
	defer server.Close()

	client := httpclient.New(httpclient.Options{AllowPrivateNetworks: true, Timeout: time.Second})
	result, err := inspectProxyPoolConfig(context.Background(), proxypool.NewResolver(client), proxypool.Config{
		APIHeaders:  []proxypool.Header{{Name: "X-Pool-Token", Value: "pool-token"}},
		APIURL:      server.URL,
		Enabled:     1,
		Protocol:    proxypool.ProtocolHTTP,
		RefreshSecs: 30,
		Source:      proxypool.SourceAPI,
	})
	if err != nil {
		t.Fatalf("inspectProxyPoolConfig() error = %v", err)
	}
	if result.DetectedPath != "data.items.0.proxy_ip" || result.Host != "203.0.113.20" || result.Port != 3128 {
		t.Fatalf("inspect result = %+v", result)
	}
	if len(result.Candidates) == 0 || result.Candidates[0].Path != "data.items.0.proxy_ip" {
		t.Fatalf("candidates = %+v", result.Candidates)
	}
}
