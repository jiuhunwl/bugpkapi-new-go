package ipconfig

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"bugpk-api/internal/domain/ipregion"
	"bugpk-api/internal/pkg/httpclient"
)

func TestNormalizeConfigAcceptsDefaultSource(t *testing.T) {
	cfg, err := normalizeConfig(Config{
		DataSourceName: "default",
		APIURL:         "",
		ParseRule:      "",
		Enabled:        1,
		Remark:         "",
	})
	if err != nil {
		t.Fatalf("normalizeConfig() error = %v", err)
	}
	if cfg.DataSourceName != "default" || cfg.Enabled != 1 || cfg.ParseRule == "" {
		t.Fatalf("unexpected default config: %#v", cfg)
	}
}

func TestNormalizeConfigRejectsCustomWithoutURLPlaceholder(t *testing.T) {
	if _, err := normalizeConfig(Config{DataSourceName: "custom_x", APIURL: "", Enabled: 1}); err == nil {
		t.Fatal("expected custom source without apiUrl to fail")
	}
	if _, err := normalizeConfig(Config{DataSourceName: "custom_x", APIURL: "https://example.com/ip", Enabled: 1}); err == nil {
		t.Fatal("expected custom source without {ip} placeholder to fail")
	}
	if _, err := normalizeConfig(Config{DataSourceName: "custom_x", APIURL: "https://example.com/{ip}", Enabled: 1, ParseRule: "country=$.country"}); err != nil {
		t.Fatalf("valid custom source rejected: %v", err)
	}
}

func TestNormalizeConfigRejectsIncompleteCustomRule(t *testing.T) {
	_, err := normalizeConfig(Config{
		DataSourceName: "custom_x",
		APIURL:         "https://example.com/{ip}",
		ParseRule:      "country=xxx, unknown=$.foo",
		Enabled:        1,
	})
	if err == nil {
		t.Fatal("incomplete custom parse rule was accepted")
	}
}

func TestNormalizeConfigRequiresCoordinateMappingsAsAPair(t *testing.T) {
	_, err := normalizeConfig(Config{
		DataSourceName: "custom_x",
		APIURL:         "https://example.com/{ip}",
		ParseRule:      "latitude=$.latitude",
		Enabled:        1,
	})
	if err == nil || !strings.Contains(err.Error(), "经纬度字段必须同时配置") {
		t.Fatalf("unpaired coordinate mapping error = %v", err)
	}
}

func TestActiveSourceFallsBackToDefault(t *testing.T) {
	source := ipregion.DefaultSourceConfig()
	if source.IsDefault() != true {
		t.Fatal("default source should be marked default")
	}
	if !source.IsDefault() {
		t.Fatal("unexpected")
	}
}

func TestParseRuleFromText(t *testing.T) {
	items := parseRuleFromText("country=$.country, province=$.regionName, city=$.city, unknown=$.x, country=$.dup")
	if len(items) != 3 {
		t.Fatalf("parseRuleFromText() = %v, want 3 valid fields", items)
	}
}

func TestProbeSourceDiscoversFieldsAndPreviewsMappings(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"data":{"country":"中国","province":"北京","city":"北京市","isp":"Example","lat":39.9042,"lng":"116.4074"}}`))
	}))
	defer server.Close()
	client := httpclient.New(httpclient.Options{AllowPrivateNetworks: true, Timeout: time.Second})

	result, err := probeSource(context.Background(), TestSourceRequest{
		APIURL: server.URL + "/lookup?ip={ip}",
		FieldMappings: map[string]string{
			"country": "$.data.country", "province": "$.data.province", "city": "$.data.city",
			"isp": "$.data.isp", "latitude": "$.data.lat", "longitude": "$.data.lng",
		},
		TestIP: "8.8.8.8",
	}, client)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Fields) != 6 {
		t.Fatalf("discovered fields = %#v", result.Fields)
	}
	if len(result.MissingPairFields) != 0 || len(result.InvalidFields) != 0 {
		t.Fatalf("mapping diagnostics = missing pair %v invalid %v", result.MissingPairFields, result.InvalidFields)
	}
	if result.Preview.Country != "中国" || result.Preview.Latitude == nil || result.Preview.Longitude == nil {
		t.Fatalf("preview = %#v", result.Preview)
	}

	pairResult, err := probeSource(context.Background(), TestSourceRequest{
		APIURL: server.URL + "/lookup?ip={ip}",
		FieldMappings: map[string]string{
			"latitude": "$.data.lat",
		},
		TestIP: "8.8.8.8",
	}, client)
	if err != nil {
		t.Fatal(err)
	}
	if len(pairResult.MissingPairFields) != 1 || pairResult.MissingPairFields[0] != "longitude" {
		t.Fatalf("missing coordinate pair = %v", pairResult.MissingPairFields)
	}
}

func TestProbeSourceBlocksProtectedNetworksAndRedirects(t *testing.T) {
	for _, target := range []string{
		"http://127.0.0.1:8080/{ip}",
		"http://10.0.0.1/{ip}",
		"http://169.254.169.254/{ip}",
		"http://[::1]:8080/{ip}",
		"http://[fe80::1]/{ip}",
	} {
		_, err := probeSource(context.Background(), TestSourceRequest{
			APIURL: target,
			TestIP: "8.8.8.8",
		}, httpclient.Outbound(time.Second))
		if err == nil || !strings.Contains(err.Error(), "受保护的网络") {
			t.Fatalf("protected target %q error = %v", target, err)
		}
	}

	redirectTarget := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{"country":"unexpected"}`))
	}))
	defer redirectTarget.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, redirectTarget.URL, http.StatusFound)
	}))
	defer redirector.Close()
	client := httpclient.New(httpclient.Options{AllowPrivateNetworks: true, Timeout: time.Second})
	_, err := probeSource(context.Background(), TestSourceRequest{
		APIURL: redirector.URL + "/{ip}",
		TestIP: "8.8.8.8",
	}, client)
	if err == nil || !strings.Contains(err.Error(), "跨域重定向") {
		t.Fatalf("cross-origin redirect error = %v", err)
	}
}

func TestProbeSourceRejectsOversizedAndAmbiguousJSON(t *testing.T) {
	client := httpclient.New(httpclient.Options{AllowPrivateNetworks: true, Timeout: time.Second})
	oversized := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{"value":"` + strings.Repeat("x", int(httpclient.MaxProviderResponseBytes)) + `"}`))
	}))
	defer oversized.Close()
	_, err := probeSource(context.Background(), TestSourceRequest{APIURL: oversized.URL + "/{ip}", TestIP: "8.8.8.8"}, client)
	if err == nil || !strings.Contains(err.Error(), "256 KiB") {
		t.Fatalf("oversized response error = %v", err)
	}

	ambiguous := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{"country":"first","country":"second"}`))
	}))
	defer ambiguous.Close()
	_, err = probeSource(context.Background(), TestSourceRequest{APIURL: ambiguous.URL + "/{ip}", TestIP: "8.8.8.8"}, client)
	if err == nil || !strings.Contains(err.Error(), "结构明确") {
		t.Fatalf("ambiguous JSON error = %v", err)
	}
}

func TestNormalizePublicTestIPRejectsLocalRanges(t *testing.T) {
	for _, ip := range []string{"127.0.0.1", "10.0.0.1", "169.254.1.1", "100.64.0.1", "::1", "fc00::1", "fe80::1"} {
		if got := normalizePublicTestIP(ip); got != "" {
			t.Fatalf("normalizePublicTestIP(%q) = %q", ip, got)
		}
	}
	if got := normalizePublicTestIP("2001:4860:4860::8888"); got == "" {
		t.Fatal("public IPv6 address was rejected")
	}
}
