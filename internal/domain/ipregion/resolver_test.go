package ipregion

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"bugpk-api/internal/config"
)

func TestRemoteProvidersRejectHTTPFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":"upstream unavailable"}`))
	}))
	defer server.Close()

	for _, provider := range []string{"bilibili", "tencent-map", "amap"} {
		t.Run(provider, func(t *testing.T) {
			resolver := newRemoteTestResolver(provider, server)
			_, err := resolver.Resolve(context.Background(), "8.8.8.8")
			if err == nil || !strings.Contains(err.Error(), "http 502") {
				t.Fatalf("Resolve() error = %v, want HTTP status failure", err)
			}
		})
	}
}

func TestRemoteProvidersRejectEmptySuccessPayload(t *testing.T) {
	tests := map[string]string{
		"bilibili":    `{"code":0,"data":{}}`,
		"tencent-map": `{"status":0,"result":{}}`,
		"amap":        `{"status":"1","province":"","city":[]}`,
	}
	for provider, payload := range tests {
		t.Run(provider, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(payload))
			}))
			defer server.Close()

			resolver := newRemoteTestResolver(provider, server)
			_, err := resolver.Resolve(context.Background(), "8.8.4.4")
			if err == nil || !strings.Contains(err.Error(), "empty data") {
				t.Fatalf("Resolve() error = %v, want empty data failure", err)
			}
		})
	}
}

func TestBilibiliProviderReturnsRegionAndForwardsIP(t *testing.T) {
	var requestedIP string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		requestedIP = request.URL.Query().Get("ip")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"data":{"country":"US","province":"CA","city":"Mountain View","isp":"Example","latitude":"37.4","longitude":"-122.1"}}`))
	}))
	defer server.Close()

	resolver := newRemoteTestResolver("bilibili", server)
	region, err := resolver.Resolve(context.Background(), "8.8.8.8")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if requestedIP != "8.8.8.8" || region.Provider != "bilibili" || region.Region != "US CA Mountain View" {
		t.Fatalf("unexpected result: requestedIP=%q region=%#v", requestedIP, region)
	}
}

func TestBilibiliProviderSupportsCurrentResponseShape(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if got := request.URL.Query().Get("ip"); got != "222.174.86.2" {
			t.Fatalf("requested ip = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"msg":"","message":"","data":{"addr":"222.174.86.2","country":"中国","province":"山东","city":"菏泽","isp":"电信","latitude":"35.23951","longitude":"115.436218"}}`))
	}))
	defer server.Close()

	resolver := newRemoteTestResolver("bilibili", server)
	region, err := resolver.Resolve(context.Background(), "222.174.86.2")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if region.Country != "中国" || region.Province != "山东" || region.City != "菏泽" || region.ISP != "电信" {
		t.Fatalf("unexpected Bilibili region: %#v", region)
	}
	if region.Latitude != 35.23951 || region.Longitude != 115.436218 {
		t.Fatalf("Bilibili coordinates were not parsed: %#v", region)
	}
}

func TestBilibiliProviderRejectsMismatchedAddress(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"data":{"addr":"1.1.1.1","country":"中国","province":"山东","city":"菏泽"}}`))
	}))
	defer server.Close()

	resolver := newRemoteTestResolver("bilibili", server)
	_, err := resolver.Resolve(context.Background(), "222.174.86.2")
	if err == nil || !strings.Contains(err.Error(), "mismatched address") {
		t.Fatalf("Resolve() error = %v, want mismatched-address failure", err)
	}
}

func TestBilibiliProviderRejectsInvalidCoordinates(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"data":{"addr":"222.174.86.2","country":"中国","province":"山东","city":"菏泽","latitude":"91.5","longitude":"115.436218"}}`))
	}))
	defer server.Close()

	resolver := newRemoteTestResolver("bilibili", server)
	_, err := resolver.Resolve(context.Background(), "222.174.86.2")
	if err == nil || !strings.Contains(err.Error(), "invalid coordinates") {
		t.Fatalf("Resolve() error = %v, want invalid-coordinate failure", err)
	}
}

func TestPrivateIPSkipsProviderRequest(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	resolver := newRemoteTestResolver("bilibili", server)
	region, err := resolver.Resolve(context.Background(), "127.0.0.1")
	if err != nil || region != (Region{}) || requests.Load() != 0 {
		t.Fatalf("private IP resolution = %#v, %v, requests=%d", region, err, requests.Load())
	}
}

func TestNonPublicIPsAreNotSentToProviders(t *testing.T) {
	for _, ip := range []string{
		"10.0.0.1",
		"100.64.0.1",
		"127.0.0.1",
		"169.254.10.20",
		"224.0.0.1",
		"::1",
		"fc00::1",
		"fe80::1",
	} {
		t.Run(ip, func(t *testing.T) {
			if !isPrivateIP(ip) {
				t.Fatalf("isPrivateIP(%q) = false", ip)
			}
		})
	}
	if isPrivateIP("8.8.8.8") {
		t.Fatal("public IP was classified as private")
	}
}

func TestUnknownProviderFailsExplicitly(t *testing.T) {
	// 未知 provider 必然回退内置 ip-api；把回退端点指向一个固定 502 的
	// 本地测试服务器，保证回退确定性失败（不依赖外网可达性，避免 flaky）。
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"status":"fail"}`))
	}))
	defer fallback.Close()

	resolver := NewResolver(config.IPRegionConfig{Enabled: true, Provider: "unknown", Timeout: time.Second, IPApiEndpoint: fallback.URL + "/{ip}"}, nil)
	resolver.client = fallback.Client()
	resolver.fallback.Enabled = false
	_, err := resolver.Resolve(context.Background(), "8.8.8.8")
	if !errors.Is(err, ErrProviderNotConfigured) {
		t.Fatalf("Resolve() error = %v, want ErrProviderNotConfigured", err)
	}
}

func newRemoteTestResolver(provider string, server *httptest.Server) *Resolver {
	cfg := config.IPRegionConfig{
		Enabled:            true,
		Provider:           provider,
		Timeout:            time.Second,
		IPApiEndpoint:      server.URL + "/{ip}",
		BilibiliEndpoint:   server.URL,
		TencentMapKey:      "test-key",
		TencentMapEndpoint: server.URL,
		AmapKey:            "test-key",
		AmapEndpoint:       server.URL,
	}
	resolver := NewResolver(cfg, nil)
	resolver.client = server.Client()
	// 单元测试显式关闭真实公网二级源，避免测试结果依赖外部网络。
	resolver.fallback.Enabled = false
	return resolver
}

func TestIPApiProviderReturnsRegionAndForwardsIP(t *testing.T) {
	var requestedPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		requestedPath = request.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success","country":"中国","regionName":"北京","city":"北京","isp":"China Unicom","lat":39.9042,"lon":116.4074}`))
	}))
	defer server.Close()

	resolver := newRemoteTestResolver("ip-api", server)
	region, err := resolver.Resolve(context.Background(), "8.8.8.8")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if requestedPath != "/8.8.8.8" {
		t.Fatalf("requested path = %q, want the ip-api endpoint with {ip} substituted", requestedPath)
	}
	if region.Country != "中国" || region.Province != "北京" || region.City != "北京" || region.ISP != "China Unicom" {
		t.Fatalf("unexpected region: %#v", region)
	}
	if region.Provider != "ip-api" || region.Region != "中国 北京 北京" {
		t.Fatalf("unexpected provider/region: %#v", region)
	}
	if region.Latitude != 39.9042 || region.Longitude != 116.4074 {
		t.Fatalf("legacy coordinates were not parsed: %#v", region)
	}
}

func TestDefaultSourceParsesHTTPSCoordinates(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"country":"中国","region":"广东","city":"深圳","latitude":22.5431,"longitude":114.0579,"connection":{"isp":"Example ISP"}}`))
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
	// 测试服务器不是 ipwho.is，因此显式构造内置字段结构进行解析。
	region, err := resolver.resolveSource(context.Background(), SourceConfig{
		Name:      "default",
		APIURL:    server.URL + "/{ip}",
		ParseRule: DefaultSourceConfig().ParseRule,
		Enabled:   true,
	}, "8.8.8.8")
	if err != nil {
		t.Fatalf("resolveSource() error = %v", err)
	}
	if region.Province != "广东" || region.City != "深圳" || region.ISP != "Example ISP" || region.Latitude != 22.5431 || region.Longitude != 114.0579 {
		t.Fatalf("unexpected default-source region: %#v", region)
	}
}

func TestCustomSourceProviderFromLoader(t *testing.T) {
	var requestedPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		requestedPath = request.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"country":"US","province":"CA","city":"Mountain View"}}`))
	}))
	defer server.Close()

	resolver := newRemoteTestResolver("bilibili", server)
	resolver.SetSourceLoader(func(context.Context) (SourceConfig, error) {
		return SourceConfig{
			Name:      "custom_demo",
			APIURL:    server.URL + "/{ip}",
			ParseRule: "country=$.data.country, province=$.data.province, city=$.data.city",
			Enabled:   true,
		}, nil
	})
	region, err := resolver.Resolve(context.Background(), "8.8.4.4")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if requestedPath != "/8.8.4.4" {
		t.Fatalf("requested path = %q, want /8.8.4.4", requestedPath)
	}
	if region.Country != "US" || region.Province != "CA" || region.City != "Mountain View" {
		t.Fatalf("unexpected region: %#v", region)
	}
	if !strings.HasPrefix(region.Provider, "custom:") {
		t.Fatalf("provider = %q, want custom: prefix", region.Provider)
	}
}

func TestConfiguredBilibiliSourceUsesDedicatedParser(t *testing.T) {
	var requestedIP string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		requestedIP = request.URL.Query().Get("ip")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"data":{"addr":"222.174.86.2","country":"中国","province":"山东","city":"菏泽","isp":"电信","latitude":"35.23951","longitude":"115.436218"}}`))
	}))
	defer server.Close()

	resolver := newRemoteTestResolver("ip-api", server)
	region, err := resolver.resolveConfiguredSource(context.Background(), SourceConfig{
		Name:      "custom_bilibili",
		APIURL:    server.URL + "/ip?ip={ip}",
		ParseRule: "country=$.wrong",
		Enabled:   true,
	}, "222.174.86.2")
	if err != nil {
		t.Fatalf("resolveConfiguredSource() error = %v", err)
	}
	if requestedIP != "222.174.86.2" {
		t.Fatalf("requested ip = %q", requestedIP)
	}
	if region.Country != "中国" || region.Province != "山东" || region.City != "菏泽" || region.ISP != "电信" {
		t.Fatalf("unexpected configured Bilibili region: %#v", region)
	}
	if region.Latitude != 35.23951 || region.Longitude != 115.436218 {
		t.Fatalf("configured Bilibili coordinates were not parsed: %#v", region)
	}
}

func TestDefaultSourceLoaderFallsBackToBuiltin(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success","country":"中国","regionName":"上海","city":"上海"}`))
	}))
	defer server.Close()

	resolver := newRemoteTestResolver("ip-api", server)
	resolver.SetSourceLoader(func(context.Context) (SourceConfig, error) {
		return DefaultSourceConfig(), nil
	})
	region, err := resolver.Resolve(context.Background(), "8.8.8.8")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if region.City != "上海" || region.Provider != "ip-api" {
		t.Fatalf("unexpected region: %#v", region)
	}
}

func TestInvalidIPReturnsEmptyWithoutError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	resolver := newRemoteTestResolver("ip-api", server)
	region, err := resolver.Resolve(context.Background(), "not-an-ip")
	if err != nil || region != (Region{}) {
		t.Fatalf("invalid IP resolution = %#v, %v", region, err)
	}
}

func TestApplyParseRuleSupportsDollarPrefix(t *testing.T) {
	body := `{"status":"success","country":"中国","regionName":"广东","city":"深圳"}`
	region, err := applyParseRule("country=$.country, province=$.regionName, city=$.city", body)
	if err != nil {
		t.Fatalf("applyParseRule() error = %v", err)
	}
	if region.Country != "中国" || region.Province != "广东" || region.City != "深圳" {
		t.Fatalf("unexpected region: %#v", region)
	}
}

func TestFailingCustomSourceFallsBackToIPAPI(t *testing.T) {
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":"upstream unavailable"}`))
	}))
	defer failing.Close()
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/8.8.4.4" {
			t.Fatalf("fallback path = %q, want /8.8.4.4", request.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success","country":"中国","regionName":"广东","city":"深圳"}`))
	}))
	defer fallback.Close()

	resolver := newRemoteTestResolver("bilibili", failing)
	resolver.cfg.IPApiEndpoint = fallback.URL + "/{ip}"
	region, err := resolver.Resolve(context.Background(), "8.8.4.4")
	if err != nil {
		t.Fatalf("Resolve() error = %v, want nil (fallback succeeded)", err)
	}
	if region.Country != "中国" || region.Province != "广东" || region.City != "深圳" {
		t.Fatalf("unexpected fallback region: %#v", region)
	}
	if !strings.Contains(region.Provider, "fallback") {
		t.Fatalf("provider = %q, want fallback marker", region.Provider)
	}
}

func TestIPAPIFailureDoesNotDoubleCall(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()

	resolver := newRemoteTestResolver("ip-api", server)
	resolver.cfg.IPApiEndpoint = server.URL + "/{ip}"
	_, err := resolver.Resolve(context.Background(), "8.8.4.4")
	if err == nil {
		t.Fatal("Resolve() error = nil, want provider error")
	}
	if calls.Load() != 1 {
		t.Fatalf("ip-api provider called %d times, want exactly 1 (no self-fallback)", calls.Load())
	}
}

func TestBuiltinFailureUsesCountryFallbackWithinTotalTimeout(t *testing.T) {
	var primaryCalls atomic.Int32
	var fallbackCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/primary/8.8.8.8":
			primaryCalls.Add(1)
			<-request.Context().Done()
		case "/country/8.8.8.8":
			fallbackCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"country_name":"United States","isp":"Example"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	resolver := NewResolver(config.IPRegionConfig{
		Enabled:       true,
		Provider:      "ip-api",
		Timeout:       600 * time.Millisecond,
		IPApiEndpoint: server.URL + "/primary/{ip}",
	}, nil)
	resolver.client = server.Client()
	resolver.fallback = SourceConfig{
		Name:      "country-fallback",
		APIURL:    server.URL + "/country/{ip}",
		ParseRule: "country=$.country_name, isp=$.isp",
		Enabled:   true,
	}

	started := time.Now()
	region, err := resolver.Resolve(context.Background(), "8.8.8.8")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if elapsed := time.Since(started); elapsed >= 600*time.Millisecond {
		t.Fatalf("resolution exceeded total timeout: %s", elapsed)
	}
	if primaryCalls.Load() != 1 || fallbackCalls.Load() != 1 {
		t.Fatalf("calls primary=%d fallback=%d", primaryCalls.Load(), fallbackCalls.Load())
	}
	if region.Country != "United States" || !strings.Contains(region.Provider, "country-fallback") {
		t.Fatalf("unexpected fallback region: %#v", region)
	}
}

func TestConcurrentResolveCoalescesProviderRequest(t *testing.T) {
	var calls atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			close(started)
			<-release
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success","country":"中国","regionName":"广东","city":"深圳"}`))
	}))
	defer server.Close()

	resolver := newRemoteTestResolver("ip-api", server)
	resolver.cfg.IPApiEndpoint = server.URL + "/{ip}"
	const workers = 16
	results := make(chan error, workers)
	var wg sync.WaitGroup
	wg.Add(workers)
	for index := 0; index < workers; index++ {
		go func() {
			defer wg.Done()
			region, err := resolver.Resolve(context.Background(), "8.8.8.8")
			if err == nil && (region.Country != "中国" || region.City != "深圳") {
				err = errors.New("unexpected coalesced region")
			}
			results <- err
		}()
	}

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("provider request did not start")
	}
	time.Sleep(50 * time.Millisecond)
	if got := calls.Load(); got != 1 {
		t.Fatalf("concurrent provider requests = %d, want 1", got)
	}
	close(release)
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatalf("coalesced Resolve() error = %v", err)
		}
	}
}

func TestBuiltinIPAPIDetection(t *testing.T) {
	for _, name := range []string{"ip-api", "IP-API", " ip-api "} {
		if !isBuiltinIPAPI(name) {
			t.Fatalf("isBuiltinIPAPI(%q) = false", name)
		}
	}
	for _, name := range []string{"bilibili", "custom:demo", ""} {
		if isBuiltinIPAPI(name) {
			t.Fatalf("isBuiltinIPAPI(%q) = true", name)
		}
	}
}
