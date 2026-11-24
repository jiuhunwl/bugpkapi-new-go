package gateway

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"bugpk-api/internal/pkg/domainrouting"
	apperrors "bugpk-api/internal/pkg/errors"
)

func TestResolveDomainRouteNormalizesGeneratedRouteIDForScopedState(t *testing.T) {
	api := InterfaceConfig{
		ID:          991,
		RequestURL:  "https://default.example/api",
		PollingMode: "ROUND_ROBIN",
		DomainRouting: DomainRoutingConfig{
			Enabled:  1,
			Fallback: domainrouting.FallbackDefaultPool,
			Routes: []DomainPollingRoute{{
				Enabled: 1,
				Match: domainrouting.MatchConfig{
					Source:  domainrouting.SourceQuery,
					Field:   "url",
					Domains: []string{"example.com"},
				},
				RequestURL: "https://route.example/api",
			}},
		},
	}

	routed, selection, err := resolveDomainRoute(api, OpenRequest{Query: map[string][]string{
		"url": {"https://v.example.com/resource"},
	}})
	if err != nil {
		t.Fatalf("resolveDomainRoute() error = %v", err)
	}
	if selection.RouteID != "route-1" || routed.domainRouteID != "route-1" {
		t.Fatalf("generated route identity was not retained: selection=%+v routeScope=%q", selection, routed.domainRouteID)
	}
	if !strings.Contains(gatewayRoundRobinKey(routed, 0), ":route:route-1:") {
		t.Fatalf("round-robin key is not scoped by generated route id: %q", gatewayRoundRobinKey(routed, 0))
	}
}

func TestDomainRouteSuccessDoesNotPersistDefaultCurrentNode(t *testing.T) {
	var calls []int
	var callsMu sync.Mutex
	service := &Service{
		httpClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			return &http.Response{
				Body:       io.NopCloser(strings.NewReader(`{"code":200}`)),
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Request:    request,
				StatusCode: http.StatusOK,
			}, nil
		})},
		updateCurrentNodeFn: func(_ context.Context, _ int64, node int) error {
			callsMu.Lock()
			calls = append(calls, node)
			callsMu.Unlock()
			return nil
		},
	}

	_, err := service.invokeUpstreams(context.Background(), InterfaceConfig{
		ID:                   992,
		RequestTimeoutMs:     500,
		PollingEnabled:       1,
		PollingMode:          "ROUND_ROBIN",
		PollingCheckEnabled:  1,
		PollingCheckField:    "code",
		PollingCheckExpected: "200",
		RequestURL:           "https://default.example/must-not-call",
		UpstreamConfigs:      []UpstreamConfig{{URL: "https://default.example/backup-must-not-call"}},
		DomainRouting: DomainRoutingConfig{
			Enabled:  1,
			Fallback: domainrouting.FallbackDefaultPool,
			Routes: []DomainPollingRoute{{
				ID: "route-a", Enabled: 1,
				Match:          domainrouting.MatchConfig{Source: domainrouting.SourceQuery, Field: "url", Domains: []string{"example.com"}},
				PollingEnabled: 1, PollingMode: "ROUND_ROBIN",
				PollingCheckEnabled: 1, PollingCheckField: "code", PollingCheckExpected: "200",
				UpstreamConfigs: []UpstreamConfig{
					{URL: "https://route-a.example/node-1"},
					{URL: "https://route-a.example/node-2"},
				},
			}},
		},
	}, http.MethodGet, OpenRequest{Query: map[string][]string{
		"url": {"https://v.example.com/resource"},
	}})
	if err != nil {
		t.Fatalf("invokeUpstreams() error = %v", err)
	}
	if err := service.Close(context.Background()); err != nil {
		t.Fatalf("wait for background work: %v", err)
	}
	callsMu.Lock()
	defer callsMu.Unlock()
	if len(calls) != 0 {
		t.Fatalf("domain-routed success updated the default pool current node: %v", calls)
	}
}

func TestDomainRouteHealthBookkeepingCarriesRouteScope(t *testing.T) {
	type healthSample struct {
		scope string
		url   string
	}
	var samples []healthSample
	var samplesMu sync.Mutex
	service := &Service{
		httpClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			return &http.Response{
				Body:       io.NopCloser(strings.NewReader(`{"code":200}`)),
				Header:     make(http.Header),
				Request:    request,
				StatusCode: http.StatusOK,
			}, nil
		})},
		healthRecordScopedFn: func(_ context.Context, _ int64, scope, upstreamURL string, _ bool, _ int64) error {
			samplesMu.Lock()
			samples = append(samples, healthSample{scope: scope, url: upstreamURL})
			samplesMu.Unlock()
			return nil
		},
	}

	_, err := service.invokeUpstreams(context.Background(), InterfaceConfig{
		ID:                   993,
		RequestTimeoutMs:     500,
		PollingEnabled:       1,
		PollingMode:          "ROUND_ROBIN",
		PollingCheckEnabled:  1,
		PollingCheckField:    "code",
		PollingCheckExpected: "200",
		DomainRouting: DomainRoutingConfig{
			Enabled:  1,
			Fallback: domainrouting.FallbackDefaultPool,
			Routes: []DomainPollingRoute{{
				ID: "route-health", Enabled: 1,
				Match:          domainrouting.MatchConfig{Source: domainrouting.SourceQuery, Field: "url", Domains: []string{"example.com"}},
				PollingEnabled: 1, PollingMode: "ROUND_ROBIN",
				PollingCheckEnabled: 1, PollingCheckField: "code", PollingCheckExpected: "200",
				UpstreamConfigs: []UpstreamConfig{{URL: "https://route-health.example/node"}},
			}},
		},
	}, http.MethodGet, OpenRequest{Query: map[string][]string{
		"url": {"https://v.example.com/resource"},
	}})
	if err != nil {
		t.Fatalf("invokeUpstreams() error = %v", err)
	}
	if err := service.Close(context.Background()); err != nil {
		t.Fatalf("wait for health bookkeeping: %v", err)
	}
	samplesMu.Lock()
	defer samplesMu.Unlock()
	if len(samples) != 1 || samples[0].scope != "route-health" || !strings.Contains(samples[0].url, "route-health.example") {
		t.Fatalf("health samples = %+v, want route-scoped sample", samples)
	}
}

func TestResolveDomainRouteHonorsRejectFallbackWhenNoRoutesExist(t *testing.T) {
	api := InterfaceConfig{
		ID:         994,
		RequestURL: "https://default.example/api",
		DomainRouting: DomainRoutingConfig{
			Enabled:  1,
			Fallback: domainrouting.FallbackReject,
		},
	}

	_, _, err := resolveDomainRoute(api, OpenRequest{Query: map[string][]string{
		"url": {"https://v.example.com/resource"},
	}})
	if err == nil {
		t.Fatal("enabled routing with REJECT fallback accepted a request without routes")
	}
	var business *apperrors.BusinessError
	if !errors.As(err, &business) || business.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("error = %T %v, want 422 BusinessError", err, err)
	}
}

func TestResolveDomainRouteUsesEnabledDefaultRouteFallback(t *testing.T) {
	api := InterfaceConfig{
		ID:         995,
		RequestURL: "https://default.example/api",
		DomainRouting: DomainRoutingConfig{
			Enabled:        1,
			Fallback:       domainrouting.FallbackDefaultRoute,
			DefaultRouteID: "safe-route",
			Routes: []DomainPollingRoute{
				{
					ID: "disabled-route", Enabled: 0, Priority: 1,
					Match:      domainrouting.MatchConfig{Domains: []string{"example.com"}},
					RequestURL: "https://disabled.example/api",
				},
				{
					ID: "safe-route", Enabled: 1, Priority: 10,
					Match:      domainrouting.MatchConfig{Domains: []string{"safe.example"}},
					RequestURL: "https://safe.example/api",
				},
			},
		},
	}

	routed, selection, err := resolveDomainRoute(api, OpenRequest{Query: map[string][]string{
		"url": {"https://unmatched.example/resource"},
	}})
	if err != nil {
		t.Fatalf("resolveDomainRoute() error = %v", err)
	}
	if selection.RouteID != "safe-route" || routed.RequestURL != "https://safe.example/api" {
		t.Fatalf("default route fallback = selection=%+v routed=%+v", selection, routed)
	}
}

func TestResolveDomainRouteRejectsInvalidFallbackBeforeMatching(t *testing.T) {
	api := InterfaceConfig{
		ID:         996,
		RequestURL: "https://default.example/api",
		DomainRouting: DomainRoutingConfig{
			Enabled:  1,
			Fallback: "NOT_A_FALLBACK",
			Routes: []DomainPollingRoute{{
				ID: "matching-route", Enabled: 1,
				Match:      domainrouting.MatchConfig{Domains: []string{"example.com"}},
				RequestURL: "https://route.example/api",
			}},
		},
	}

	_, _, err := resolveDomainRoute(api, OpenRequest{Query: map[string][]string{
		"url": {"https://v.example.com/resource"},
	}})
	if err == nil {
		t.Fatal("invalid fallback was accepted because a route matched")
	}
	var business *apperrors.BusinessError
	if !errors.As(err, &business) || business.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("error = %T %v, want 503 BusinessError", err, err)
	}
}
