package gateway

import (
	"net/http"
	"strings"
	"testing"

	"bugpk-api/internal/pkg/domainrouting"
)

// TestResponseCacheKeyUsesTheResolvedDomainRoute verifies the contract used by
// Open: domain routing is resolved before the cache key is built.  Two route
// pools may share the same interface id and request shape, but their upstream
// policy (and therefore the response that is safe to reuse) is independent.
// A cache key built from the un-routed interface would let a response from one
// pool bypass polling on the other pool.
func TestResponseCacheKeyUsesTheResolvedDomainRoute(t *testing.T) {
	api := InterfaceConfig{
		ID:                   9901,
		RequestURL:           "https://default.example/api",
		CacheEnabled:         1,
		PollingEnabled:       1,
		PollingMode:          "PRIMARY",
		PollingCheckEnabled:  1,
		PollingCheckField:    "code",
		PollingCheckExpected: "200",
		DomainRouting: DomainRoutingConfig{
			Enabled:  1,
			Fallback: domainrouting.FallbackDefaultPool,
			Routes: []DomainPollingRoute{
				{
					ID: "route-a", Enabled: 1, Priority: 1,
					Match:          domainrouting.MatchConfig{Source: domainrouting.SourceQuery, Field: "url", Mode: domainrouting.MatchSubdomain, Domains: []string{"a.example"}},
					RequestURL:     "https://pool-a.example/api",
					PollingEnabled: 1, PollingMode: "PRIMARY",
					PollingCheckEnabled: 1, PollingCheckField: "code", PollingCheckExpected: "200",
					UpstreamConfigs: []UpstreamConfig{{URL: "https://pool-a-backup.example/api"}},
				},
				{
					ID: "route-b", Enabled: 1, Priority: 2,
					Match:          domainrouting.MatchConfig{Source: domainrouting.SourceQuery, Field: "url", Mode: domainrouting.MatchSubdomain, Domains: []string{"b.example"}},
					RequestURL:     "https://pool-b.example/api",
					PollingEnabled: 1, PollingMode: "PRIMARY",
					PollingCheckEnabled: 1, PollingCheckField: "code", PollingCheckExpected: "200",
					UpstreamConfigs: []UpstreamConfig{{URL: "https://pool-b-backup.example/api"}},
				},
			},
		},
	}

	reqA := OpenRequest{Method: http.MethodGet, Query: map[string][]string{"url": {"https://v.a.example/item"}}}
	reqB := OpenRequest{Method: http.MethodGet, Query: map[string][]string{"url": {"https://v.b.example/item"}}}
	routedA, selectionA, err := resolveDomainRoute(api, reqA)
	if err != nil {
		t.Fatalf("resolve route A: %v", err)
	}
	routedB, selectionB, err := resolveDomainRoute(api, reqB)
	if err != nil {
		t.Fatalf("resolve route B: %v", err)
	}
	if selectionA.RouteID != "route-a" || selectionB.RouteID != "route-b" {
		t.Fatalf("unexpected route selections: A=%+v B=%+v", selectionA, selectionB)
	}

	keyA, err := responseCacheKey(routedA, http.MethodGet, reqA)
	if err != nil {
		t.Fatalf("build route A cache key: %v", err)
	}
	keyB, err := responseCacheKey(routedB, http.MethodGet, reqB)
	if err != nil {
		t.Fatalf("build route B cache key: %v", err)
	}
	if keyA == keyB {
		t.Fatalf("resolved route pools shared a response cache key: %q", keyA)
	}
	if !strings.Contains(keyA, "gateway:response-cache:") || !strings.Contains(keyB, "gateway:response-cache:") {
		t.Fatalf("unexpected response cache keys: A=%q B=%q", keyA, keyB)
	}
}

// TestResponseCachePollingMatcherUsesResolvedRoutePolicy guards the other
// half of the cache path.  A code=500 payload must not be considered a cache
// hit when the selected route expects code=200, even if the base interface has
// a different (legacy) polling toggle.
func TestResponseCachePollingMatcherUsesResolvedRoutePolicy(t *testing.T) {
	api := InterfaceConfig{
		ID:                  9902,
		RequestURL:          "https://default.example/api",
		PollingEnabled:      0,
		PollingCheckEnabled: 0,
		DomainRouting: DomainRoutingConfig{
			Enabled:  1,
			Fallback: domainrouting.FallbackDefaultPool,
			Routes: []DomainPollingRoute{{
				ID: "strict", Enabled: 1, Priority: 1,
				Match:          domainrouting.MatchConfig{Source: domainrouting.SourceQuery, Field: "url", Domains: []string{"strict.example"}},
				RequestURL:     "https://strict.example/api",
				PollingEnabled: 1, PollingMode: "PRIMARY",
				PollingCheckEnabled: 1, PollingCheckField: "code", PollingCheckExpected: "200",
			}},
		},
	}
	req := OpenRequest{Method: http.MethodGet, Query: map[string][]string{"url": {"https://v.strict.example/item"}}}
	routed, _, err := resolveDomainRoute(api, req)
	if err != nil {
		t.Fatalf("resolve strict route: %v", err)
	}
	entry := responseCacheEntry{Body: `{"code":500,"message":"temporary"}`, ResponseStatus: http.StatusOK, Success: true}
	if responseCacheEntryMatchesPolling(routed, entry) {
		t.Fatalf("resolved strict route accepted a non-success business code: %+v", routed)
	}
}
