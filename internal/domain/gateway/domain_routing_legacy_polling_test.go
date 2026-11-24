package gateway

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"bugpk-api/internal/pkg/domainrouting"
)

func TestInvokeUpstreamsInfersPollingForLegacyDomainRouteWithEmptyMode(t *testing.T) {
	for _, test := range []struct {
		name     string
		stopCode string
	}{
		{name: "upstream pool implies polling"},
		{name: "stop code also implies polling", stopCode: "404"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls []string
			service := &Service{httpClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				calls = append(calls, request.URL.String())
				body := `{"code":500,"message":"temporary"}`
				if strings.Contains(request.URL.Host, "node-b") {
					body = `{"code":200,"data":{"ok":true}}`
				}
				return &http.Response{
					Body:       io.NopCloser(strings.NewReader(body)),
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Request:    request,
					StatusCode: http.StatusOK,
				}, nil
			})}}

			result, err := service.invokeUpstreams(context.Background(), InterfaceConfig{
				ID:         1301,
				RequestURL: "https://default.example/must-not-call",
				DomainRouting: DomainRoutingConfig{
					Enabled:  1,
					Fallback: domainrouting.FallbackDefaultPool,
					Routes: []DomainPollingRoute{{
						ID: "legacy-route", Enabled: 1,
						Match:           domainrouting.MatchConfig{Source: domainrouting.SourceQuery, Field: "url", Domains: []string{"example.com"}},
						RequestURL:      "https://legacy-primary.example/must-not-call",
						PollingEnabled:  0,
						PollingMode:     "",
						PollingStopCode: test.stopCode,
						UpstreamConfigs: []UpstreamConfig{
							{URL: "https://node-a.example/api"},
							{URL: "https://node-b.example/api"},
						},
					}},
				},
			}, http.MethodGet, OpenRequest{Query: map[string][]string{
				"url": {"https://v.example.com/resource"},
			}})

			if err != nil {
				t.Fatalf("legacy route backup success should be returned: %v", err)
			}
			if len(calls) != 2 || !strings.Contains(calls[0], "node-a.example") || !strings.Contains(calls[1], "node-b.example") {
				t.Fatalf("legacy route did not poll its complete upstream pool: calls=%v", calls)
			}
			if !result.Success || !result.UpstreamSwitched || !strings.Contains(result.UpstreamURL, "node-b.example") {
				t.Fatalf("unexpected legacy route fallback result: %+v", result)
			}
		})
	}
}

func TestInvokeUpstreamsKeepsExplicitSingleDomainRouteDisabled(t *testing.T) {
	var calls []string
	service := &Service{httpClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls = append(calls, request.URL.String())
		return &http.Response{
			Body:       io.NopCloser(strings.NewReader(`{"code":500,"message":"temporary"}`)),
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Request:    request,
			StatusCode: http.StatusOK,
		}, nil
	})}}

	_, _ = service.invokeUpstreams(context.Background(), InterfaceConfig{
		ID:         1302,
		RequestURL: "https://default.example/must-not-call",
		DomainRouting: DomainRoutingConfig{
			Enabled:  1,
			Fallback: domainrouting.FallbackDefaultPool,
			Routes: []DomainPollingRoute{{
				ID: "disabled-route", Enabled: 1,
				Match:          domainrouting.MatchConfig{Source: domainrouting.SourceQuery, Field: "url", Domains: []string{"example.com"}},
				RequestURL:     "https://route-primary.example/api",
				PollingEnabled: 0,
				PollingMode:    "SINGLE",
				UpstreamConfigs: []UpstreamConfig{
					{URL: "https://stale-backup.example/api"},
				},
			}},
		},
	}, http.MethodGet, OpenRequest{Query: map[string][]string{
		"url": {"https://v.example.com/resource"},
	}})

	if len(calls) != 1 || !strings.Contains(calls[0], "route-primary.example") {
		t.Fatalf("explicit SINGLE domain route unexpectedly polled stale backup: calls=%v", calls)
	}
}
