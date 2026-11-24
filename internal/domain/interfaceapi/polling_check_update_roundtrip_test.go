package interfaceapi

import (
	"encoding/json"
	"testing"

	"bugpk-api/internal/pkg/jsoncleaning"
)

func TestGeneralEditPreservesPerNodeChecks(t *testing.T) {
	current := InterfaceItem{
		PollingEnabled: 1, PollingMode: "PRIMARY",
		PollingCheckEnabled: 1, PollingCheckField: "meta.code", PollingCheckExpected: "0", PollingStopCode: "40401",
		UpstreamConfigs: []UpstreamConfig{{
			URL: "https://backup.example/api", PollingCheckEnabled: 1,
			PollingCheckField: "result.status", PollingCheckExpected: "ok",
			ResponseFieldMappings: []jsoncleaning.FieldMapping{{Source: "result.data", Target: "data"}},
		}},
	}
	for _, tc := range []struct {
		name, payload   string
		enabled         int
		field, expected string
		count           int
	}{
		{"legacy URL list", `{"upstreamUrls":["https://backup.example/api"]}`, 1, "result.status", "ok", 1},
		{"URL-only objects", `{"upstreamConfigs":[{"url":"https://backup.example/api"}]}`, 1, "result.status", "ok", 1},
		{"explicit disabled flag", `{"upstreamConfigs":[{"url":"https://backup.example/api","pollingCheckEnabled":0}]}`, 0, "result.status", "ok", 1},
		{"only field changed", `{"upstreamConfigs":[{"url":"https://backup.example/api","pollingCheckField":"response.code"}]}`, 1, "response.code", "ok", 1},
		{"explicit replacement", `{"upstreamConfigs":[{"url":"https://backup.example/api","pollingCheckEnabled":1,"pollingCheckField":"code","pollingCheckExpected":"201"}]}`, 1, "code", "201", 1},
		{"new node uses defaults", `{"upstreamConfigs":[{"url":"https://new.example/api"}]}`, 0, "code", "200", 1},
		{"explicit removal", `{"pollingEnabled":1,"upstreamConfigs":[],"upstreamUrls":[]}`, 0, "", "", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var req SaveInterfaceRequest
			if err := json.Unmarshal([]byte(tc.payload), &req); err != nil {
				t.Fatal(err)
			}
			preserveMissingPollingCheck(&req, current)
			preserveMissingResponseFieldMappings(&req.ResponseFieldMappings, &req.UpstreamConfigs, req.UpstreamURLs, current)
			cfg, err := normalizePolling(pollingRequestFromSave(req))
			if err != nil {
				t.Fatal(err)
			}
			if cfg.PollingCheckEnabled != 1 || cfg.PollingCheckField != "meta.code" || cfg.PollingCheckExpected != "0" || cfg.PollingStopCode != "40401" {
				t.Fatalf("main check changed during general edit: %+v", cfg)
			}
			if len(cfg.UpstreamConfigs) != tc.count {
				t.Fatalf("nodes=%d, want %d", len(cfg.UpstreamConfigs), tc.count)
			}
			if tc.count == 0 {
				return
			}
			got := cfg.UpstreamConfigs[0]
			if got.PollingCheckEnabled != tc.enabled || got.PollingCheckField != tc.field || got.PollingCheckExpected != tc.expected {
				t.Fatalf("node response check lost/overwritten: %+v; want enabled=%d field=%s expected=%s", got, tc.enabled, tc.field, tc.expected)
			}
			if got.URL == current.UpstreamConfigs[0].URL && (len(got.ResponseFieldMappings) != 1 || got.ResponseFieldMappings[0].Source != "result.data") {
				t.Fatalf("node response mappings were lost: %+v", got)
			}
		})
	}
}

func TestPollingEditorCanExplicitlyClearNodeChecks(t *testing.T) {
	var req SavePollingConfig
	if err := json.Unmarshal([]byte(`{"pollingEnabled":1,"pollingMode":"PRIMARY","pollingCheckEnabled":0,"pollingStopCode":"","upstreamConfigs":[{"url":"https://backup.example/api","pollingCheckEnabled":0,"pollingCheckField":"","pollingCheckExpected":""}]}`), &req); err != nil {
		t.Fatal(err)
	}
	cfg, err := normalizePolling(req)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PollingCheckEnabled != 0 || cfg.PollingStopCode != "" || cfg.UpstreamConfigs[0].PollingCheckEnabled != 0 {
		t.Fatalf("polling editor cannot clear check settings: %+v", cfg)
	}
}
