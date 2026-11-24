package interfaceapi

import (
	"encoding/json"
	"reflect"
	"testing"

	"bugpk-api/internal/pkg/domainrouting"
)

func TestPreservedLegacyDomainRouteKeepsCandidatesAfterSaveRoundTrip(t *testing.T) {
	for _, tt := range []struct {
		name string
		mode string
		want []string
	}{
		{"legacy inferred pool", "", []string{"https://node-a.example/api", "https://node-b.example/api"}},
		{"explicitly disabled", "SINGLE", []string{"https://primary.example/api"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			current := DomainRoutingConfig{Enabled: 1, Fallback: domainrouting.FallbackDefaultPool, Routes: []DomainPollingRoute{{
				ID: "legacy", Enabled: 1, RequestURL: "https://primary.example/api", PollingEnabled: 0, PollingMode: tt.mode,
				Match:           domainrouting.MatchConfig{Source: domainrouting.SourceQuery, Field: "url", Domains: []string{"example.com"}},
				UpstreamConfigs: []UpstreamConfig{{URL: "https://node-a.example/api"}, {URL: "https://node-b.example/api"}},
			}}}
			var submitted *DomainRoutingConfig
			preserveMissingDomainRouting(&submitted, current)
			cfg, err := normalizePolling(SavePollingConfig{DomainRouting: submitted})
			if err != nil {
				t.Fatal(err)
			}
			stored, err := json.Marshal(cfg.DomainRouting)
			if err != nil {
				t.Fatal(err)
			}
			var restored DomainRoutingConfig
			if err := json.Unmarshal(stored, &restored); err != nil {
				t.Fatal(err)
			}
			routed, _, err := resolveInterfaceDomainRoute(InterfaceItem{DomainRouting: restored}, InvokePayload{QueryParams: map[string]any{"url": "https://v.example.com/a"}})
			if err != nil {
				t.Fatal(err)
			}
			if got := upstreamURLs(upstreamCandidates(routed)); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("saved candidates=%v, want %v", got, tt.want)
			}
			if current.Routes[0].PollingMode != tt.mode {
				t.Fatal("preservation mutated the existing config")
			}
		})
	}
}

func TestDomainRoutingOnlyUpdatePreservesDisabledDefaultPool(t *testing.T) {
	current := InterfaceItem{RequestURL: "https://primary.example/api", PollingEnabled: 0, PollingMode: "SINGLE", UpstreamConfigs: []UpstreamConfig{{URL: "https://backup.example/api"}}}
	req := SaveInterfaceRequest{DomainRouting: &DomainRoutingConfig{Enabled: 0, Fallback: domainrouting.FallbackDefaultPool}}
	if !hasPollingPayload(req) {
		t.Fatal("domain routing must be saved")
	}
	preserveMissingPollingCheck(&req, current)
	preserveMissingResponseFieldMappings(&req.ResponseFieldMappings, &req.UpstreamConfigs, req.UpstreamURLs, current)
	preserveMissingDomainRouting(&req.DomainRouting, current.DomainRouting)
	cfg, err := normalizePolling(pollingRequestFromSave(req))
	if err != nil {
		t.Fatal(err)
	}
	result := InterfaceItem{RequestURL: current.RequestURL}
	applyPollingConfig(&result, cfg)
	if result.PollingEnabled != 0 || result.PollingMode != "SINGLE" {
		t.Fatalf("route-only save changed default polling: enabled=%d mode=%s", result.PollingEnabled, result.PollingMode)
	}
	if got := upstreamURLs(upstreamCandidates(result)); !reflect.DeepEqual(got, []string{current.RequestURL}) {
		t.Fatalf("disabled default pool reached backup candidates: %v", got)
	}
}
