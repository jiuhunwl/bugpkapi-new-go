package gateway

import (
	"testing"

	"bugpk-api/internal/pkg/domainrouting"
)

func TestResponseCacheKeyChangesWhenDomainRoutingPolicyChanges(t *testing.T) {
	base := InterfaceConfig{
		ID:           903,
		RequestURL:   "https://aggregate.example/api",
		CacheEnabled: 1,
		DomainRouting: DomainRoutingConfig{Enabled: 1, Fallback: domainrouting.FallbackDefaultPool, Routes: []DomainPollingRoute{{
			ID: "douyin", Enabled: 1, Priority: 1,
			Match:      domainrouting.MatchConfig{Domains: []string{"douyin.com"}},
			RequestURL: "https://provider-a.example/api",
		}}},
	}
	first, err := responseCacheKey(base, "GET", OpenRequest{})
	if err != nil {
		t.Fatalf("first cache key: %v", err)
	}
	base.DomainRouting.Routes[0].RequestURL = "https://provider-b.example/api"
	second, err := responseCacheKey(base, "GET", OpenRequest{})
	if err != nil {
		t.Fatalf("second cache key: %v", err)
	}
	if first == second {
		t.Fatal("domain routing change reused the old response cache key")
	}
}

func TestResponseCacheKeyIgnoresDomainRoutingRuntimeHealthMetadata(t *testing.T) {
	base := InterfaceConfig{
		ID:           904,
		RequestURL:   "https://aggregate.example/api",
		CacheEnabled: 1,
		DomainRouting: DomainRoutingConfig{Enabled: 1, Fallback: domainrouting.FallbackDefaultPool, Routes: []DomainPollingRoute{
			{
				ID: "douyin", Name: "Douyin pool", Enabled: 1, Priority: 10,
				Match:      domainrouting.MatchConfig{Source: domainrouting.SourceQuery, Field: "url", Mode: domainrouting.MatchSubdomain, ValueMode: domainrouting.ValueModeAll, Domains: []string{"douyin.com"}},
				RequestURL: "https://provider.example/douyin",
				UpstreamConfigs: []UpstreamConfig{{
					URL: "https://node.example/douyin", HealthStatus: "HEALTHY", FailureCount: 2, LastLatencyMs: 17, LastCheckedAt: "2026-09-04T10:00:00Z",
				}},
			},
		}},
	}
	first, err := responseCacheKey(base, "GET", OpenRequest{})
	if err != nil {
		t.Fatalf("first cache key: %v", err)
	}
	base.DomainRouting.Routes[0].Name = "renamed operational label"
	base.DomainRouting.Routes[0].UpstreamConfigs[0].HealthStatus = "UNHEALTHY"
	base.DomainRouting.Routes[0].UpstreamConfigs[0].FailureCount = 99
	base.DomainRouting.Routes[0].UpstreamConfigs[0].LastLatencyMs = 9999
	base.DomainRouting.Routes[0].UpstreamConfigs[0].LastCheckedAt = "2026-09-04T11:00:00Z"
	second, err := responseCacheKey(base, "GET", OpenRequest{})
	if err != nil {
		t.Fatalf("second cache key: %v", err)
	}
	if first != second {
		t.Fatalf("runtime health metadata or route label changed cache key: first=%s second=%s", first, second)
	}
}

func TestResponseCacheKeyPreservesDomainRouteOrderAndPrioritySemantics(t *testing.T) {
	base := InterfaceConfig{
		ID:           905,
		RequestURL:   "https://aggregate.example/api",
		CacheEnabled: 1,
		DomainRouting: DomainRoutingConfig{Enabled: 1, Fallback: domainrouting.FallbackDefaultPool, Routes: []DomainPollingRoute{
			{ID: "first", Enabled: 1, Priority: 10, Match: domainrouting.MatchConfig{Domains: []string{"example.com"}}, RequestURL: "https://first.example/api"},
			{ID: "second", Enabled: 1, Priority: 10, Match: domainrouting.MatchConfig{Domains: []string{"example.com"}}, RequestURL: "https://second.example/api"},
		}},
	}
	first, err := responseCacheKey(base, "GET", OpenRequest{})
	if err != nil {
		t.Fatalf("first cache key: %v", err)
	}
	base.DomainRouting.Routes[0], base.DomainRouting.Routes[1] = base.DomainRouting.Routes[1], base.DomainRouting.Routes[0]
	second, err := responseCacheKey(base, "GET", OpenRequest{})
	if err != nil {
		t.Fatalf("second cache key: %v", err)
	}
	if first == second {
		t.Fatal("changing same-priority route order reused the old cache key")
	}
	base.DomainRouting.Routes[0].Priority = 1
	third, err := responseCacheKey(base, "GET", OpenRequest{})
	if err != nil {
		t.Fatalf("third cache key: %v", err)
	}
	if second == third {
		t.Fatal("changing route priority reused the old cache key")
	}
}
