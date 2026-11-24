package interfaceapi

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"bugpk-api/internal/pkg/domainrouting"
	"bugpk-api/internal/pkg/upstreamhealth"
)

type interfaceProbeTimeoutError struct{}

func (interfaceProbeTimeoutError) Error() string   { return "probe timeout" }
func (interfaceProbeTimeoutError) Timeout() bool   { return true }
func (interfaceProbeTimeoutError) Temporary() bool { return true }

func TestProbeOneReportsHealthyFor2xxNode(t *testing.T) {
	service := &Service{httpClient: &http.Client{Transport: interfaceRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: http.NoBody}, nil
	})}}
	result := service.probeOne(context.Background(), InterfaceItem{RequestMethod: "GET"}, UpstreamConfig{URL: "https://node-a.example.com/api"})
	if result.HealthStatus != upstreamhealth.StatusHealthy {
		t.Fatalf("HealthStatus = %q, want HEALTHY; result=%+v", result.HealthStatus, result)
	}
	if result.LatencyMs < 0 {
		t.Fatalf("LatencyMs = %d, want >= 0", result.LatencyMs)
	}
	if result.Message != "ok" {
		t.Fatalf("Message = %q, want ok", result.Message)
	}
	if result.CheckedAt == "" {
		t.Fatal("CheckedAt is empty for a healthy probe")
	}
}

func TestProbeOneReportsUnhealthyFor5xxNode(t *testing.T) {
	service := &Service{httpClient: &http.Client{Transport: interfaceRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusInternalServerError, Header: make(http.Header), Body: http.NoBody}, nil
	})}}
	result := service.probeOne(context.Background(), InterfaceItem{RequestMethod: "GET"}, UpstreamConfig{URL: "https://node-a.example.com/api"})
	if result.HealthStatus != upstreamhealth.StatusUnhealthy {
		t.Fatalf("HealthStatus = %q, want UNHEALTHY; result=%+v", result.HealthStatus, result)
	}
	if result.StatusCode != http.StatusInternalServerError {
		t.Fatalf("StatusCode = %d, want 500", result.StatusCode)
	}
	if !strings.Contains(result.Message, "500") {
		t.Fatalf("Message = %q, want it to mention HTTP 500", result.Message)
	}
}

func TestProbeOneReportsTimeoutForNetTimeout(t *testing.T) {
	service := &Service{httpClient: &http.Client{Transport: interfaceRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, interfaceProbeTimeoutError{}
	})}}
	result := service.probeOne(context.Background(), InterfaceItem{RequestMethod: "GET"}, UpstreamConfig{URL: "https://node-a.example.com/api"})
	if result.HealthStatus != upstreamhealth.StatusUnhealthy {
		t.Fatalf("HealthStatus = %q, want UNHEALTHY; result=%+v", result.HealthStatus, result)
	}
	if result.Message != "连接超时" {
		t.Fatalf("Message = %q, want 连接超时", result.Message)
	}
}

func TestProbeOneIgnoresBusinessValidationForHealthCheck(t *testing.T) {
	// 健康探测是无参数请求，只以 HTTP 状态码判定节点可达性，
	// 即使配置了 pollingCheck 业务校验（code=200），2xx 也应为 HEALTHY。
	service := &Service{httpClient: &http.Client{Transport: interfaceRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"code":"999"}`))}, nil
	})}}
	result := service.probeOne(context.Background(), InterfaceItem{RequestMethod: "GET"}, UpstreamConfig{
		URL: "https://node-a.example.com/api", PollingCheckEnabled: 1, PollingCheckField: "code", PollingCheckExpected: "200",
	})
	if result.HealthStatus != upstreamhealth.StatusHealthy {
		t.Fatalf("HealthStatus = %q, want HEALTHY; result=%+v", result.HealthStatus, result)
	}
	if result.Message != "ok" {
		t.Fatalf("Message = %q, want ok", result.Message)
	}
}

func TestProbeOneReportsUnhealthyFor4xx(t *testing.T) {
	service := &Service{httpClient: &http.Client{Transport: interfaceRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusNotFound, Header: make(http.Header), Body: http.NoBody}, nil
	})}}
	result := service.probeOne(context.Background(), InterfaceItem{RequestMethod: "GET"}, UpstreamConfig{URL: "https://node-a.example.com/api"})
	if result.HealthStatus != upstreamhealth.StatusUnhealthy {
		t.Fatalf("HealthStatus = %q, want UNHEALTHY; result=%+v", result.HealthStatus, result)
	}
	if result.Message != "HTTP 404" {
		t.Fatalf("Message = %q, want HTTP 404", result.Message)
	}
}

func TestProbeOnePostsEmptyObjectBodyForPOST(t *testing.T) {
	var body []byte
	service := &Service{httpClient: &http.Client{Transport: interfaceRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", request.Method)
		}
		if request.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("content-type = %q, want application/json", request.Header.Get("Content-Type"))
		}
		body, _ = io.ReadAll(request.Body)
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: http.NoBody}, nil
	})}}
	service.probeOne(context.Background(), InterfaceItem{RequestMethod: "POST"}, UpstreamConfig{URL: "https://node-a.example.com/api"})
	if string(body) != "{}" {
		t.Fatalf("body = %q, want {}", string(body))
	}
}

func TestProbeInterfaceNodesWorksWithoutRedis(t *testing.T) {
	service := &Service{httpClient: &http.Client{Transport: interfaceRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		status := http.StatusOK
		if strings.Contains(request.URL.String(), "backup") {
			status = http.StatusServiceUnavailable
		}
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: http.NoBody}, nil
	})}}
	item := InterfaceItem{
		ID:              "42",
		PollingEnabled:  1,
		PollingMode:     "PRIMARY",
		RequestMethod:   "GET",
		RequestURL:      "https://primary.example.com/api",
		UpstreamConfigs: []UpstreamConfig{{URL: "https://backup.example.com/api"}},
	}
	results, err := service.probeInterfaceNodes(context.Background(), item)
	if err != nil {
		t.Fatalf("probeInterfaceNodes error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("len(results) = %d, want 2", len(results))
	}
	if results[0].HealthStatus != upstreamhealth.StatusHealthy || results[1].HealthStatus != upstreamhealth.StatusUnhealthy {
		t.Fatalf("unexpected results: %+v", results)
	}
	if results[1].StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("backup node StatusCode = %d, want 503", results[1].StatusCode)
	}
}

func TestProbeInterfaceNodesIncludesEnabledDomainRoutePools(t *testing.T) {
	var calls []string
	service := &Service{httpClient: &http.Client{Transport: interfaceRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls = append(calls, request.URL.String())
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: http.NoBody}, nil
	})}}
	item := InterfaceItem{
		ID:             "43",
		PollingEnabled: 0,
		RequestMethod:  "GET",
		RequestURL:     "https://default.example/api",
		DomainRouting: DomainRoutingConfig{
			Enabled: 1,
			Routes: []DomainPollingRoute{
				{
					ID: "douyin", Name: "抖音池", Enabled: 1, Priority: 1,
					Match:          domainrouting.MatchConfig{Domains: []string{"douyin.com"}},
					PollingEnabled: 1, PollingMode: "ROUND_ROBIN",
					UpstreamConfigs: []UpstreamConfig{
						{URL: "https://douyin-a.example/api"},
						{URL: "https://douyin-b.example/api"},
					},
				},
				{
					ID: "disabled", Name: "停用池", Enabled: 0, Priority: 2,
					Match:      domainrouting.MatchConfig{Domains: []string{"disabled.example"}},
					RequestURL: "https://disabled.example/api",
				},
			},
		},
	}
	results, err := service.probeInterfaceNodes(context.Background(), item)
	if err != nil {
		t.Fatalf("probeInterfaceNodes error: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("len(results) = %d, want default plus two enabled-route nodes", len(results))
	}
	if results[0].RouteID != "" || results[0].RouteName != "" {
		t.Fatalf("default pool result unexpectedly has route metadata: %+v", results[0])
	}
	for _, result := range results[1:] {
		if result.RouteID != "douyin" || result.RouteName != "抖音池" {
			t.Fatalf("route result missing identity: %+v", result)
		}
	}
	if len(calls) != 3 || strings.Contains(strings.Join(calls, "\n"), "disabled.example") {
		t.Fatalf("probe calls = %v", calls)
	}
}
