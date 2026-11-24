package interfaceapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"bugpk-api/internal/pkg/domainrouting"
	apperrors "bugpk-api/internal/pkg/errors"
	"bugpk-api/internal/pkg/jsoncleaning"
	"bugpk-api/internal/pkg/proxypool"
	"bugpk-api/internal/pkg/secretbox"

	"github.com/cloudwego/hertz/pkg/protocol/consts"
	mysqlDriver "github.com/go-sql-driver/mysql"
)

func TestInterfaceChangedCallbackReceivesOldAndNewAPICodes(t *testing.T) {
	service := &Service{}
	var gotOld, gotNew string
	service.SetInterfaceChanged(func(oldAPICode, newAPICode string) {
		gotOld, gotNew = oldAPICode, newAPICode
	})

	service.notifyInterfaceChanged("old-code", "new-code")
	if gotOld != "old-code" || gotNew != "new-code" {
		t.Fatalf("interface change callback received (%q, %q), want (%q, %q)", gotOld, gotNew, "old-code", "new-code")
	}
}

func TestNormalizePollingRejectsInvalidBinaryFlags(t *testing.T) {
	invalidPollingEnabled := 2
	if _, err := normalizePolling(SavePollingConfig{PollingEnabled: &invalidPollingEnabled}); err == nil {
		t.Fatal("expected invalid pollingEnabled to be rejected")
	}

	invalidCheckEnabled := 2
	if _, err := normalizePolling(SavePollingConfig{PollingCheckEnabled: &invalidCheckEnabled}); err == nil {
		t.Fatal("expected invalid pollingCheckEnabled to be rejected")
	}
}

func TestNormalizeInterfaceQPSLimit(t *testing.T) {
	for _, value := range []int{-1, 0, 1, maxInterfaceQPSLimit} {
		value := value
		got, err := normalizeInterfaceQPSLimit(&value)
		if err != nil || got != value {
			t.Fatalf("normalizeInterfaceQPSLimit(%d) = %d, %v", value, got, err)
		}
	}
	if got, err := normalizeInterfaceQPSLimit(nil); err != nil || got != 0 {
		t.Fatalf("normalizeInterfaceQPSLimit(nil) = %d, %v", got, err)
	}
	for _, value := range []int{-2, maxInterfaceQPSLimit + 1} {
		value := value
		if _, err := normalizeInterfaceQPSLimit(&value); err == nil {
			t.Fatalf("normalizeInterfaceQPSLimit(%d) unexpectedly succeeded", value)
		}
	}
}

func TestNormalizePollingAcceptsDefaultAndBinaryFlags(t *testing.T) {
	enabled := 1
	checkEnabled := 0
	cfg, err := normalizePolling(SavePollingConfig{
		PollingCheckEnabled: &checkEnabled,
		PollingEnabled:      &enabled,
	})
	if err != nil {
		t.Fatalf("normalizePolling returned error: %v", err)
	}
	if cfg.PollingEnabled != 1 || cfg.PollingCheckEnabled != 0 {
		t.Fatalf("unexpected polling flags: %#v", cfg)
	}
}

func TestNormalizePollingValidatesProxyPoolConfiguration(t *testing.T) {
	enabled := 1
	cfg, err := normalizePolling(SavePollingConfig{
		PollingEnabled: &enabled,
		ProxyPoolConfig: proxypool.Config{
			APIHeaders:  []proxypool.Header{{Name: "Authorization", Value: "Bearer token"}},
			APIURL:      "https://pool.example.com/next",
			Enabled:     1,
			Protocol:    proxypool.ProtocolSOCKS5,
			RefreshSecs: 45,
			Source:      proxypool.SourceAPI,
		},
	})
	if err != nil {
		t.Fatalf("normalizePolling() error = %v", err)
	}
	if cfg.ProxyPoolConfig.Source != proxypool.SourceAPI || cfg.ProxyPoolConfig.RefreshSecs != 45 {
		t.Fatalf("proxy pool config = %+v", cfg.ProxyPoolConfig)
	}

	_, err = normalizePolling(SavePollingConfig{ProxyPoolConfig: proxypool.Config{
		Enabled: 1, Source: proxypool.SourceFixed, Protocol: "FTP", Host: "proxy.example.com", Port: 21,
	}})
	if err == nil {
		t.Fatal("invalid proxy protocol was accepted")
	}
}

func TestNormalizePollingAcceptsFastestMode(t *testing.T) {
	enabled := 1
	cfg, err := normalizePolling(SavePollingConfig{
		PollingEnabled:  &enabled,
		PollingMode:     "FASTEST",
		UpstreamConfigs: []UpstreamConfig{{URL: "https://node-a.example.com/api"}},
	})
	if err != nil {
		t.Fatalf("normalizePolling returned error: %v", err)
	}
	if cfg.PollingMode != "FASTEST" {
		t.Fatalf("PollingMode = %q, want FASTEST", cfg.PollingMode)
	}
}

func TestNormalizePollingCanonicalizesExplicitDisabledModeToSingle(t *testing.T) {
	disabled := 0
	cfg, err := normalizePolling(SavePollingConfig{
		PollingEnabled:  &disabled,
		PollingMode:     "PRIMARY",
		PollingStopCode: "404",
		UpstreamConfigs: []UpstreamConfig{{URL: "https://backup.example.com/api"}},
	})
	if err != nil {
		t.Fatalf("normalizePolling returned error: %v", err)
	}
	if cfg.PollingEnabled != 0 || cfg.PollingMode != "SINGLE" {
		t.Fatalf("explicitly disabled polling must persist as SINGLE, got %+v", cfg)
	}
}

func TestNormalizePollingRejectsUnknownMode(t *testing.T) {
	enabled := 1
	if _, err := normalizePolling(SavePollingConfig{PollingEnabled: &enabled, PollingMode: "NOT_A_MODE"}); err == nil {
		t.Fatal("expected unknown polling mode to be rejected")
	}
}

func TestNormalizePollingCarriesStopCodeAndTrimsWhitespace(t *testing.T) {
	enabled := 1
	cfg, err := normalizePolling(SavePollingConfig{
		PollingEnabled:  &enabled,
		PollingMode:     "PRIMARY",
		PollingStopCode: "  40401  ",
	})
	if err != nil {
		t.Fatalf("normalizePolling returned error: %v", err)
	}
	if cfg.PollingStopCode != "40401" {
		t.Fatalf("PollingStopCode = %q, want trimmed 40401", cfg.PollingStopCode)
	}
	tooLong := strings.Repeat("1", 65)
	if _, err := normalizePolling(SavePollingConfig{PollingEnabled: &enabled, PollingStopCode: tooLong}); err == nil {
		t.Fatal("expected over-length polling stop code to be rejected")
	}
}

func TestNormalizePollingUsesRuneLengths(t *testing.T) {
	enabled := 1
	valid := strings.Repeat("字段", 64)
	if _, err := normalizePolling(SavePollingConfig{PollingEnabled: &enabled, PollingCheckField: valid}); err != nil {
		t.Fatalf("128-rune polling field rejected: %v", err)
	}
	if _, err := normalizePolling(SavePollingConfig{PollingEnabled: &enabled, PollingCheckField: valid + "多"}); err == nil {
		t.Fatal("129-rune polling field was accepted")
	}
}

func TestNormalizePollingRejectsOversizedPerNodeCheckValues(t *testing.T) {
	enabled := 1
	_, err := normalizePolling(SavePollingConfig{
		PollingEnabled: &enabled,
		UpstreamConfigs: []UpstreamConfig{{
			URL:                  "https://node-a.example.com/api",
			PollingCheckField:    strings.Repeat("f", 129),
			PollingCheckExpected: "200",
		}},
	})
	if err == nil {
		t.Fatal("expected oversized per-node health-check field to be rejected")
	}
}

func TestFastestCandidatesIncludePrimaryURL(t *testing.T) {
	item := InterfaceItem{
		PollingEnabled:  1,
		PollingMode:     "FASTEST",
		RequestURL:      "https://primary.example.com/api",
		UpstreamConfigs: []UpstreamConfig{{URL: "https://backup.example.com/api"}},
	}
	candidates := upstreamCandidates(item)
	if len(candidates) != 2 {
		t.Fatalf("len(candidates) = %d, want 2", len(candidates))
	}
	if candidates[0].URL != item.RequestURL {
		t.Fatalf("first candidate = %q, want primary %q", candidates[0].URL, item.RequestURL)
	}
}

func TestDecodeUpstreamConfigsSupportsLegacyStringArray(t *testing.T) {
	configs := decodeUpstreamConfigs(`["https://node-a.example.com/api","https://node-b.example.com/api"]`)
	if len(configs) != 2 {
		t.Fatalf("len(configs) = %d, want 2", len(configs))
	}
	if configs[1].URL != "https://node-b.example.com/api" {
		t.Fatalf("configs[1].URL = %q", configs[1].URL)
	}
}

func TestDomainRoutingDocumentRoundTripsWithLegacyUpstreamFields(t *testing.T) {
	routing := DomainRoutingConfig{
		Enabled:        1,
		Fallback:       "DEFAULT_ROUTE",
		DefaultRouteID: "douyin",
		Routes: []DomainPollingRoute{{
			ID:       "douyin",
			Name:     "抖音专用池",
			Enabled:  1,
			Priority: 10,
			Match: domainrouting.MatchConfig{
				Source:    domainrouting.SourceAuto,
				Field:     "payload.url",
				Mode:      domainrouting.MatchSubdomain,
				ValueMode: domainrouting.ValueModeAll,
				Domains:   []string{"douyin.com"},
			},
			RequestURL:              "https://provider.example/douyin",
			PollingEnabled:          1,
			PollingMode:             "ROUND_ROBIN",
			PollingCheckEnabled:     1,
			PollingCheckField:       "code",
			PollingCheckExpected:    "200",
			ResponseCleaningEnabled: 1,
			ResponseFieldMappings:   []jsoncleaning.FieldMapping{{Source: "data.url", Target: "url"}},
			UpstreamConfigs: []UpstreamConfig{{
				URL:                  "https://node.example/douyin",
				PollingCheckEnabled:  1,
				PollingCheckField:    "code",
				PollingCheckExpected: "200",
			}},
		}},
	}
	encoded, err := encodeUpstreamConfigDocumentWithProxyAndRouting(
		[]jsoncleaning.FieldMapping{{Source: "data.id", Target: "id"}},
		[]UpstreamConfig{{URL: "https://legacy.example/api"}},
		proxypool.Config{},
		routing,
	)
	if err != nil {
		t.Fatalf("encode domain routing document: %v", err)
	}
	primary, configs, _, decoded := decodeUpstreamConfigDocumentFullWithRouting(string(encoded))
	if len(primary) != 1 || primary[0].Target != "id" {
		t.Fatalf("primary mappings = %#v", primary)
	}
	if len(configs) != 1 || configs[0].URL != "https://legacy.example/api" {
		t.Fatalf("legacy upstream configs = %#v", configs)
	}
	if decoded.Enabled != 1 || decoded.Fallback != "DEFAULT_ROUTE" || decoded.DefaultRouteID != "douyin" {
		t.Fatalf("decoded routing = %+v", decoded)
	}
	if len(decoded.Routes) != 1 || decoded.Routes[0].Match.Domains[0] != "douyin.com" ||
		decoded.Routes[0].UpstreamConfigs[0].URL != "https://node.example/douyin" {
		t.Fatalf("decoded route = %+v", decoded.Routes)
	}
}

func TestNormalizePollingNormalizesDomainRoutingAndRoutePolling(t *testing.T) {
	enabled := 1
	cfg, err := normalizePolling(SavePollingConfig{
		PollingEnabled: &enabled,
		DomainRouting: &DomainRoutingConfig{
			Enabled:        1,
			Fallback:       "default_route",
			DefaultRouteID: "douyin",
			Routes: []DomainPollingRoute{{
				ID:       "douyin",
				Name:     " Douyin ",
				Enabled:  1,
				Priority: 9,
				Match: domainrouting.MatchConfig{
					Source:    "auto",
					Field:     "payload.url",
					Mode:      "subdomain",
					ValueMode: "all",
					Domains:   []string{" WWW.DOUYIN.COM ", "douyin.com"},
				},
				RequestURL:           "https://provider.example/douyin",
				PollingEnabled:       1,
				PollingMode:          "round_robin",
				PollingCheckEnabled:  1,
				PollingCheckField:    "code",
				PollingCheckExpected: "200",
				UpstreamConfigs: []UpstreamConfig{{
					URL: "https://node.example/douyin",
				}},
			}},
		},
	})
	if err != nil {
		t.Fatalf("normalizePolling() error = %v", err)
	}
	if cfg.DomainRouting.Enabled != 1 || cfg.DomainRouting.Fallback != "DEFAULT_ROUTE" {
		t.Fatalf("routing settings = %+v", cfg.DomainRouting)
	}
	route := cfg.DomainRouting.Routes[0]
	if route.Match.Source != domainrouting.SourceAuto || route.Match.Mode != domainrouting.MatchSubdomain ||
		route.Match.ValueMode != domainrouting.ValueModeAll || len(route.Match.Domains) != 2 || route.Match.Domains[0] != "www.douyin.com" {
		t.Fatalf("normalized match = %+v", route.Match)
	}
	if route.PollingMode != "ROUND_ROBIN" || route.UpstreamConfigs[0].PollingCheckField != "code" {
		t.Fatalf("normalized route polling = %+v", route)
	}
}

func TestNormalizePollingCanonicalizesExplicitDisabledDomainRouteToSingle(t *testing.T) {
	enabled := 1
	cfg, err := normalizePolling(SavePollingConfig{
		PollingEnabled: &enabled,
		DomainRouting: &DomainRoutingConfig{
			Enabled:  1,
			Fallback: domainrouting.FallbackDefaultPool,
			Routes: []DomainPollingRoute{{
				ID:              "disabled-route",
				Name:            "Disabled route",
				Enabled:         1,
				Match:           domainrouting.MatchConfig{Domains: []string{"example.com"}},
				RequestURL:      "https://primary.example.com/api",
				PollingEnabled:  0,
				PollingMode:     "PRIMARY",
				PollingStopCode: "404",
				UpstreamConfigs: []UpstreamConfig{{URL: "https://backup.example.com/api"}},
			}},
		},
	})
	if err != nil {
		t.Fatalf("normalizePolling returned error: %v", err)
	}
	route := cfg.DomainRouting.Routes[0]
	if route.PollingEnabled != 0 || route.PollingMode != "SINGLE" {
		t.Fatalf("explicitly disabled route must persist as SINGLE, got %+v", route)
	}
}

func TestNormalizePollingRejectsInvalidDomainRoutingFallbackAndDefaultRoute(t *testing.T) {
	enabled := 1
	base := DomainRoutingConfig{
		Enabled: 1,
		Routes: []DomainPollingRoute{{
			ID: "known", Enabled: 1,
			Match:      domainrouting.MatchConfig{Domains: []string{"example.com"}},
			RequestURL: "https://provider.example/api",
		}},
	}
	invalidFallback := base
	invalidFallback.Fallback = "NOT_A_FALLBACK"
	if _, err := normalizePolling(SavePollingConfig{PollingEnabled: &enabled, DomainRouting: &invalidFallback}); err == nil {
		t.Fatal("invalid domain routing fallback was accepted")
	}
	missingDefault := base
	missingDefault.Fallback = "DEFAULT_ROUTE"
	missingDefault.DefaultRouteID = "missing"
	if _, err := normalizePolling(SavePollingConfig{PollingEnabled: &enabled, DomainRouting: &missingDefault}); err == nil {
		t.Fatal("missing default route was accepted")
	}
	duplicate := base
	duplicate.Routes = append(duplicate.Routes, duplicate.Routes[0])
	if _, err := normalizePolling(SavePollingConfig{PollingEnabled: &enabled, DomainRouting: &duplicate}); err == nil {
		t.Fatal("duplicate domain route ID was accepted")
	}
}

func TestNormalizeDomainRoutingDefaultsOmittedPriorityButPreservesExplicitZero(t *testing.T) {
	const routeJSON = `{"id":"priority-route","enabled":1,"match":{"domains":["example.com"]},"requestUrl":"https://provider.example/api"}`
	decode := func(priorityPresent bool) PollingConfig {
		raw := `{"pollingEnabled":1,"domainRouting":{"enabled":1,"routes":[` + routeJSON
		if priorityPresent {
			raw = `{"pollingEnabled":1,"domainRouting":{"enabled":1,"routes":[{"id":"priority-route","enabled":1,"priority":0,"match":{"domains":["example.com"]},"requestUrl":"https://provider.example/api"}`
		}
		raw += `]}}`
		var req SavePollingConfig
		if err := json.Unmarshal([]byte(raw), &req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		cfg, err := normalizePolling(req)
		if err != nil {
			t.Fatalf("normalizePolling() error = %v", err)
		}
		return cfg
	}

	if got := decode(false).DomainRouting.Routes[0].Priority; got != 100 {
		t.Fatalf("omitted priority = %d, want default 100", got)
	}
	if got := decode(true).DomainRouting.Routes[0].Priority; got != 0 {
		t.Fatalf("explicit priority zero = %d, want 0", got)
	}
}

func TestUpstreamConfigDocumentKeepsPrimaryAndPerNodeMappings(t *testing.T) {
	primary := []jsoncleaning.FieldMapping{{Source: "payload", Target: "data"}}
	configs := []UpstreamConfig{{
		URL:                   "https://node-a.example.com/api",
		ResponseFieldMappings: []jsoncleaning.FieldMapping{{Source: "media", Target: "url"}},
	}}
	encoded, err := encodeUpstreamConfigDocument(primary, configs)
	if err != nil {
		t.Fatal(err)
	}
	decodedPrimary, decodedConfigs := decodeUpstreamConfigDocument(string(encoded))
	if len(decodedPrimary) != 1 || decodedPrimary[0] != primary[0] {
		t.Fatalf("primary mappings = %#v", decodedPrimary)
	}
	if len(decodedConfigs) != 1 || len(decodedConfigs[0].ResponseFieldMappings) != 1 || decodedConfigs[0].ResponseFieldMappings[0] != configs[0].ResponseFieldMappings[0] {
		t.Fatalf("upstream configs = %#v", decodedConfigs)
	}
}

func TestUpstreamConfigDocumentPreservesProxyPool(t *testing.T) {
	proxyConfig := proxypool.Config{
		Enabled: 1, Source: proxypool.SourceFixed, Protocol: proxypool.ProtocolHTTP,
		Host: "proxy.example.com", Port: 8080, Username: "user", Password: "enc:v1:ciphertext",
	}
	encoded, err := encodeUpstreamConfigDocumentWithProxy(nil, []UpstreamConfig{{URL: "https://node.example/api"}}, proxyConfig)
	if err != nil {
		t.Fatal(err)
	}
	_, configs, decodedProxy := decodeUpstreamConfigDocumentFull(string(encoded))
	if len(configs) != 1 || decodedProxy.Host != proxyConfig.Host || decodedProxy.Password != proxyConfig.Password {
		t.Fatalf("decoded configs=%+v proxy=%+v", configs, decodedProxy)
	}
}

func TestProxyPoolPasswordIsEncryptedForStorageAndRedactedForManagement(t *testing.T) {
	cipher := secretbox.New("interface-proxy-secret-that-is-at-least-32-bytes")
	input := proxypool.Config{Enabled: 1, Source: proxypool.SourceFixed, Protocol: proxypool.ProtocolHTTP, Host: "proxy.example.com", Port: 8080, Username: "alice", Password: "plain-secret"}
	encoded, err := encodeUpstreamConfigDocumentForStorage(cipher, nil, nil, input, "")
	if err != nil {
		t.Fatalf("encodeUpstreamConfigDocumentForStorage() error = %v", err)
	}
	_, _, stored := decodeUpstreamConfigDocumentFull(string(encoded))
	if stored.Password == "plain-secret" || !secretbox.IsEncrypted(stored.Password) {
		t.Fatalf("stored proxy password = %q, want encrypted", stored.Password)
	}
	runtime, err := decryptProxyPoolConfig(cipher, stored)
	if err != nil || runtime.Password != "plain-secret" {
		t.Fatalf("decryptProxyPoolConfig() = %+v, %v", runtime, err)
	}
	public := redactProxyPoolConfig(stored)
	if public.Password != "" || !public.PasswordConfigured {
		t.Fatalf("redacted proxy config = %+v", public)
	}
}

func TestProxyPoolPasswordPreservesExistingCiphertextWhenFormOmitsPassword(t *testing.T) {
	cipher := secretbox.New("interface-proxy-secret-that-is-at-least-32-bytes")
	existing, err := cipher.Encrypt("keep-me")
	if err != nil {
		t.Fatal(err)
	}
	input := proxypool.Config{Enabled: 1, Source: proxypool.SourceFixed, Protocol: proxypool.ProtocolHTTP, Host: "proxy.example.com", Port: 8080, PasswordConfigured: true}
	encoded, err := encodeUpstreamConfigDocumentForStorage(cipher, nil, nil, input, existing)
	if err != nil {
		t.Fatalf("encodeUpstreamConfigDocumentForStorage() error = %v", err)
	}
	_, _, stored := decodeUpstreamConfigDocumentFull(string(encoded))
	decrypted, err := cipher.DecryptCompatible(stored.Password)
	if err != nil || decrypted != "keep-me" {
		t.Fatalf("stored password = %q err=%v", decrypted, err)
	}
}

func TestProxyPoolPasswordClearsExistingCiphertextWhenFormRequestsClear(t *testing.T) {
	cipher := secretbox.New("interface-proxy-secret-that-is-at-least-32-bytes")
	existing, err := cipher.Encrypt("remove-me")
	if err != nil {
		t.Fatal(err)
	}
	input := proxypool.Config{
		Enabled:            1,
		Source:             proxypool.SourceFixed,
		Protocol:           proxypool.ProtocolHTTP,
		Host:               "proxy.example.com",
		Port:               8080,
		Password:           "",
		PasswordConfigured: false,
	}
	encoded, err := encodeUpstreamConfigDocumentForStorage(cipher, nil, nil, input, existing)
	if err != nil {
		t.Fatalf("encodeUpstreamConfigDocumentForStorage() error = %v", err)
	}
	_, _, stored := decodeUpstreamConfigDocumentFull(string(encoded))
	if stored.Password != "" {
		t.Fatalf("stored password = %q, want empty", stored.Password)
	}
}

func TestNormalizePollingTrimsAndValidatesFieldMappings(t *testing.T) {
	cfg, err := normalizePolling(SavePollingConfig{
		ResponseFieldMappings: []jsoncleaning.FieldMapping{{Source: " payload ", Target: " data "}},
		UpstreamConfigs: []UpstreamConfig{{
			URL:                   "https://node-a.example.com/api",
			ResponseFieldMappings: []jsoncleaning.FieldMapping{{Source: "media_link", Target: "url"}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ResponseFieldMappings[0] != (jsoncleaning.FieldMapping{Source: "payload", Target: "data"}) {
		t.Fatalf("primary mappings = %#v", cfg.ResponseFieldMappings)
	}
	if cfg.UpstreamConfigs[0].ResponseFieldMappings[0].Target != "url" {
		t.Fatalf("upstream mappings = %#v", cfg.UpstreamConfigs)
	}

	_, err = normalizePolling(SavePollingConfig{ResponseFieldMappings: []jsoncleaning.FieldMapping{
		{Source: "first", Target: "url"},
		{Source: "second", Target: "url"},
	}})
	if err == nil {
		t.Fatal("duplicate target mapping was accepted")
	}
}

func TestApplyTemplateReportsMissingParameterName(t *testing.T) {
	_, err := applyTemplate("https://example.com/weather/{city}", map[string]any{})
	if err == nil {
		t.Fatal("expected missing template parameter error")
	}
	if !strings.Contains(err.Error(), "city") {
		t.Fatalf("error %q does not mention missing parameter", err.Error())
	}
}

func TestInvokeUpstreamsDoesNotPoisonFallbackHealthForMissingParameter(t *testing.T) {
	called := false
	service := &Service{httpClient: &http.Client{Transport: interfaceRoundTripFunc(func(*http.Request) (*http.Response, error) {
		called = true
		return nil, errors.New("upstream must not be called")
	})}}
	primary := "https://primary.example.com/weather/{city}"
	result, err := service.invokeUpstreams(context.Background(), InterfaceItem{
		ID:              "1",
		PollingEnabled:  1,
		PollingMode:     "PRIMARY",
		RequestMethod:   http.MethodGet,
		RequestURL:      primary,
		UpstreamConfigs: []UpstreamConfig{{URL: "https://backup.example.com/weather/{city}"}},
	}, http.MethodGet, InvokePayload{QueryParams: map[string]any{}})
	if err == nil {
		t.Fatal("missing template parameter was accepted")
	}
	var business *apperrors.BusinessError
	if !errors.As(err, &business) || business.StatusCode != consts.StatusBadRequest {
		t.Fatalf("error = %T %v, want 400 BusinessError", err, err)
	}
	if called {
		t.Fatal("upstream HTTP request was sent despite missing client parameter")
	}
	if result.UpstreamURL != primary || result.UpstreamSwitched {
		t.Fatalf("missing parameter incorrectly advanced to fallback: %+v", result)
	}
}

func TestInvokeUpstreamsUsesOnlyTheMatchingDomainRoutePool(t *testing.T) {
	var calls []string
	service := &Service{httpClient: &http.Client{Transport: interfaceRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls = append(calls, request.URL.String())
		body := `{"code":500}`
		if strings.Contains(request.URL.Host, "douyin-b") {
			body = `{"code":200}`
		}
		return &http.Response{
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Request:    request,
			StatusCode: http.StatusOK,
		}, nil
	})}}
	item := InterfaceItem{
		ID:                   "44",
		RequestMethod:        http.MethodGet,
		RequestTimeoutMs:     1000,
		RequestURL:           "https://default.example/must-not-call",
		PollingEnabled:       1,
		PollingMode:          "ROUND_ROBIN",
		PollingCheckEnabled:  1,
		PollingCheckField:    "code",
		PollingCheckExpected: "200",
		UpstreamConfigs:      []UpstreamConfig{{URL: "https://default.example/backup-must-not-call"}},
		DomainRouting: DomainRoutingConfig{
			Enabled:  1,
			Fallback: "DEFAULT_POOL",
			Routes: []DomainPollingRoute{{
				ID: "douyin", Name: "抖音池", Enabled: 1, Priority: 1,
				Match:          domainrouting.MatchConfig{Source: domainrouting.SourceQuery, Field: "url", Domains: []string{"douyin.com"}},
				PollingEnabled: 1, PollingMode: "ROUND_ROBIN",
				PollingCheckEnabled: 1, PollingCheckField: "code", PollingCheckExpected: "200",
				UpstreamConfigs: []UpstreamConfig{
					{URL: "https://douyin-a.example/api"},
					{URL: "https://douyin-b.example/api"},
				},
			}},
		},
	}
	result, err := service.invokeUpstreams(context.Background(), item, http.MethodGet, InvokePayload{
		QueryParams: map[string]any{"url": "https://v.douyin.com/video/1"},
	})
	if err != nil {
		t.Fatalf("invokeUpstreams error: %v", err)
	}
	if !result.Success || !strings.Contains(result.UpstreamURL, "douyin-b.example") {
		t.Fatalf("result = %+v", result)
	}
	if len(calls) != 2 || !strings.Contains(calls[0], "douyin-a.example") || !strings.Contains(calls[1], "douyin-b.example") {
		t.Fatalf("calls = %v", calls)
	}
	if strings.Contains(strings.Join(calls, "\n"), "default.example") {
		t.Fatalf("default pool was called: %v", calls)
	}
}

func TestInvokeUpstreamsStopsFallbackWhenCallerContextIsCanceled(t *testing.T) {
	service := &Service{httpClient: &http.Client{Transport: interfaceRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		return nil, request.Context().Err()
	})}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	primary := "https://primary.example.com/api"
	result, err := service.invokeUpstreams(ctx, InterfaceItem{
		ID:              "1",
		PollingEnabled:  1,
		PollingMode:     "PRIMARY",
		RequestMethod:   http.MethodGet,
		RequestURL:      primary,
		UpstreamConfigs: []UpstreamConfig{{URL: "https://backup.example.com/api"}},
	}, http.MethodGet, InvokePayload{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context canceled", err)
	}
	if result.UpstreamURL != primary || result.UpstreamSwitched {
		t.Fatalf("canceled request incorrectly advanced to fallback: %+v", result)
	}
}

func TestResponsePassesUsesExactArrayPathValues(t *testing.T) {
	cfg := UpstreamConfig{
		PollingCheckEnabled:  1,
		PollingCheckField:    "data.items.0.id",
		PollingCheckExpected: "9007199254740993",
	}
	if !responsePasses(cfg, http.StatusOK, `{"data":{"items":[{"id":9007199254740993}]}}`) {
		t.Fatal("exact large integer array path did not pass")
	}
	if responsePasses(cfg, http.StatusOK, `{"data":{"items":[{"id":9007199254740992}]}}`) {
		t.Fatal("different large integer passed after precision loss")
	}
	if responsePasses(UpstreamConfig{PollingCheckEnabled: 1, PollingCheckField: "missing", PollingCheckExpected: ""}, http.StatusOK, `{}`) {
		t.Fatal("missing response field matched an empty expected value")
	}
}

func TestInterfaceOpenTrafficConditionExcludesAdminTestLogs(t *testing.T) {
	got := interfaceOpenTrafficCondition("l")
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
		t.Fatalf("interface open traffic predicate should stay a static SQL fragment, got %q", got)
	}

	plain := interfaceOpenTrafficCondition("")
	if strings.Contains(plain, "l.charge_type") || !strings.Contains(plain, "charge_type") {
		t.Fatalf("unexpected unaliased predicate: %q", plain)
	}
}

func TestMapInterfaceWriteErrorMapsDuplicateCodeToConflict(t *testing.T) {
	err := mapInterfaceWriteError(&mysqlDriver.MySQLError{Number: 1062, Message: "duplicate api_code"})
	var business *apperrors.BusinessError
	if !errors.As(err, &business) {
		t.Fatalf("expected BusinessError, got %T", err)
	}
	if business.StatusCode != consts.StatusConflict {
		t.Fatalf("status = %d, want %d", business.StatusCode, consts.StatusConflict)
	}
}

func TestValidateAvatarURLRequiresGeneratedLocalFilename(t *testing.T) {
	prefix := "/system-api/upload/interface-avatar/"
	valid := prefix + strings.Repeat("a", 32) + ".png"
	if err := validateAvatarURL(valid, "/system-api"); err != nil {
		t.Fatalf("generated local avatar URL rejected: %v", err)
	}
	for _, value := range []string{
		prefix + "avatar.png",
		prefix + "../secret.png",
		prefix + strings.Repeat("a", 32) + ".png?download=1",
	} {
		if err := validateAvatarURL(value, "/system-api"); err == nil {
			t.Fatalf("invalid local avatar URL accepted: %q", value)
		}
	}
	if err := validateAvatarURL("https://cdn.example.com/avatar.png", "/system-api"); err != nil {
		t.Fatalf("external HTTPS avatar URL rejected: %v", err)
	}
}
