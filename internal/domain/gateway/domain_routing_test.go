package gateway

import (
	"context"
	stderrors "errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"bugpk-api/internal/pkg/domainrouting"
	apperrors "bugpk-api/internal/pkg/errors"
)

func TestResolveDomainRouteUsesSelectedPriorityPool(t *testing.T) {
	api := InterfaceConfig{
		ID:             901,
		RequestURL:     "https://default.example/aggregate",
		PollingEnabled: 1,
		PollingMode:    "ROUND_ROBIN",
		UpstreamConfigs: []UpstreamConfig{{
			URL: "https://default.example/fallback",
		}},
		DomainRouting: DomainRoutingConfig{
			Enabled:  1,
			Fallback: domainrouting.FallbackDefaultPool,
			Routes: []DomainPollingRoute{
				{
					ID: "wide", Enabled: 1, Priority: 100,
					Match:          domainrouting.MatchConfig{Domains: []string{"example.com"}},
					RequestURL:     "https://wide.example/provider",
					PollingEnabled: 0,
				},
				{
					ID: "exact", Enabled: 1, Priority: 10,
					Match:          domainrouting.MatchConfig{Mode: domainrouting.MatchExact, Domains: []string{"api.example.com"}},
					RequestURL:     "https://exact.example/provider",
					PollingEnabled: 0,
				},
			},
		},
	}
	routed, selection, err := resolveDomainRoute(api, OpenRequest{Query: map[string][]string{
		"url": {"https://api.example.com/video/1"},
	}})
	if err != nil {
		t.Fatalf("resolveDomainRoute() error = %v", err)
	}
	if selection.RouteID != "exact" || selection.MatchedDomain != "api.example.com" {
		t.Fatalf("selection = %+v", selection)
	}
	if routed.RequestURL != "https://exact.example/provider" || len(routed.UpstreamConfigs) != 0 || routed.PollingMode != "SINGLE" {
		t.Fatalf("routed API = %+v", routed)
	}
}

func TestResolveDomainRouteRejectFallbackReturnsStructuredNoChargeError(t *testing.T) {
	api := InterfaceConfig{
		RequestURL: "https://default.example/aggregate",
		DomainRouting: DomainRoutingConfig{
			Enabled:  1,
			Fallback: domainrouting.FallbackReject,
			Routes: []DomainPollingRoute{{
				ID: "douyin", Enabled: 1,
				Match:      domainrouting.MatchConfig{Domains: []string{"douyin.com"}},
				RequestURL: "https://provider.example/douyin",
			}},
		},
	}
	_, _, err := resolveDomainRoute(api, OpenRequest{Query: map[string][]string{
		"url": {"https://www.kuaishou.com/video/1"},
	}})
	if err == nil {
		t.Fatal("unmatched domain was accepted")
	}
	var business *apperrors.BusinessError
	if !stderrors.As(err, &business) {
		t.Fatalf("error type = %T", err)
	}
	if business.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d", business.StatusCode)
	}
	data, ok := business.Data.(map[string]any)
	if !ok || data["tag"] != "DOMAIN_ROUTE_MISMATCH" {
		t.Fatalf("error data = %#v", business.Data)
	}
}

func TestInvokeUpstreamsOnlyCallsSelectedDomainPoolAndFallsBackInsideIt(t *testing.T) {
	var calls []string
	service := &Service{httpClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls = append(calls, request.URL.String())
		body := `{"code":500}`
		if strings.Contains(request.URL.Host, "node-b") {
			body = `{"code":200}`
		}
		return &http.Response{
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     make(http.Header),
			Request:    request,
			StatusCode: http.StatusOK,
		}, nil
	})}}
	api := InterfaceConfig{
		ID:                   902,
		RequestURL:           "https://default.example/should-not-call",
		PollingEnabled:       1,
		PollingMode:          "ROUND_ROBIN",
		PollingCheckEnabled:  1,
		PollingCheckField:    "code",
		PollingCheckExpected: "200",
		UpstreamConfigs:      []UpstreamConfig{{URL: "https://default.example/should-not-call-2"}},
		DomainRouting: DomainRoutingConfig{
			Enabled:  1,
			Fallback: domainrouting.FallbackDefaultPool,
			Routes: []DomainPollingRoute{{
				ID: "douyin", Enabled: 1, Priority: 1,
				Match:          domainrouting.MatchConfig{Domains: []string{"douyin.com"}},
				PollingEnabled: 1, PollingMode: "ROUND_ROBIN",
				PollingCheckEnabled: 1, PollingCheckField: "code", PollingCheckExpected: "200",
				UpstreamConfigs: []UpstreamConfig{
					{URL: "https://node-a.example/douyin"},
					{URL: "https://node-b.example/douyin"},
				},
			}},
		},
	}
	result, err := service.invokeUpstreams(context.Background(), api, http.MethodGet, OpenRequest{Query: map[string][]string{
		"url": {"https://v.douyin.com/video/1"},
	}})
	if err != nil {
		t.Fatalf("invokeUpstreams() error = %v", err)
	}
	if !result.Success || !strings.Contains(result.UpstreamURL, "node-b.example") {
		t.Fatalf("result = %+v", result)
	}
	if len(calls) != 2 || !strings.Contains(calls[0], "node-a.example") || !strings.Contains(calls[1], "node-b.example") {
		t.Fatalf("calls = %v", calls)
	}
	for _, call := range calls {
		if strings.Contains(call, "default.example") {
			t.Fatalf("default pool was called: %v", calls)
		}
	}
}
