package gateway

import (
	"testing"

	"bugpk-api/internal/pkg/domainrouting"
)

func TestDecodeInterfaceConfigIncludesDomainRoutingRoutes(t *testing.T) {
	_, configs, _, routing := decodeUpstreamConfigDocumentFullWithRouting(`{
		"domainRouting": {
			"enabled": 1,
			"fallback": "REJECT",
			"defaultRouteId": "douyin",
			"routes": [{
				"id": "douyin",
				"name": "Douyin",
				"enabled": 1,
				"priority": 5,
				"match": {"source":"QUERY","field":"url","mode":"SUBDOMAIN","valueMode":"ALL","domains":["douyin.com"]},
				"requestUrl": "https://provider.example/douyin",
				"pollingEnabled": 1,
				"pollingMode": "ROUND_ROBIN",
				"upstreamConfigs": [{"url":"https://node.example/douyin"}]
			}]
		},
		"upstreams": [{"url":"https://default.example/api"}]
	}`)
	if len(configs) != 1 || configs[0].URL != "https://default.example/api" {
		t.Fatalf("default configs = %+v", configs)
	}
	if routing.Enabled != 1 || routing.Fallback != "REJECT" || routing.DefaultRouteID != "douyin" || len(routing.Routes) != 1 {
		t.Fatalf("routing = %+v", routing)
	}
	route := routing.Routes[0]
	if route.ID != "douyin" || route.Match.Mode != domainrouting.MatchSubdomain || route.UpstreamConfigs[0].URL != "https://node.example/douyin" {
		t.Fatalf("route = %+v", route)
	}
}
