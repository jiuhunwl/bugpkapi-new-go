package gateway

import (
	"encoding/json"
	"testing"
)

func TestDomainRouteJSONPriorityIsHonoredByGateway(t *testing.T) {
	for _, tt := range []struct {
		name          string
		firstPriority string
		want          string
	}{
		{"explicit zero", `,"priority":0`, "first"},
		{"omitted priority", "", "second"},
		{"null priority", `,"priority":null`, "second"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			raw := `{"enabled":1,"fallback":"DEFAULT_POOL","routes":[
   {"id":"first","enabled":1` + tt.firstPriority + `,"requestUrl":"https://first.example/api","match":{"source":"QUERY","field":"url","domains":["example.com"]}},
   {"id":"second","enabled":1,"priority":10,"requestUrl":"https://second.example/api","match":{"source":"QUERY","field":"url","domains":["example.com"]}}
  ]}`
			var routing DomainRoutingConfig
			if err := json.Unmarshal([]byte(raw), &routing); err != nil {
				t.Fatal(err)
			}
			stored, err := json.Marshal(routing)
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(stored, &routing); err != nil {
				t.Fatal(err)
			}
			got, selection, err := resolveDomainRoute(InterfaceConfig{DomainRouting: routing}, OpenRequest{Query: map[string][]string{"url": {"https://v.example.com/a"}}})
			if err != nil {
				t.Fatal(err)
			}
			if selection.RouteID != tt.want || got.RequestURL != "https://"+tt.want+".example/api" {
				t.Fatalf("selected route=%s url=%s, want %s", selection.RouteID, got.RequestURL, tt.want)
			}
		})
	}
}
