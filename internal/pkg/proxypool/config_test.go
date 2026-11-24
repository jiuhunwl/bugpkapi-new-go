package proxypool

import "testing"

func TestNormalizeDisabledFixedProxyPreservesConfiguration(t *testing.T) {
	cfg, err := Normalize(Config{
		APIHeaders:         []Header{{Name: "X-Ignored", Value: "ignored"}},
		APIURL:             "https://pool.example.com/ignored",
		Enabled:            0,
		Host:               " proxy.example.com ",
		JSONPath:           "data.proxy",
		Password:           " secret ",
		PasswordConfigured: true,
		Port:               8080,
		Protocol:           " http ",
		RefreshSecs:        60,
		Source:             " fixed ",
		Username:           " alice ",
	})
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	if cfg.Enabled != 0 || cfg.Source != SourceFixed || cfg.Protocol != ProtocolHTTP || cfg.Host != "proxy.example.com" || cfg.Port != 8080 {
		t.Fatalf("normalized disabled fixed proxy = %+v", cfg)
	}
	if cfg.Username != "alice" || cfg.Password != "secret" || !cfg.PasswordConfigured {
		t.Fatalf("normalized disabled credentials = %+v", cfg)
	}
	if cfg.APIURL != "" || cfg.APIHeaders != nil || cfg.JSONPath != "" || cfg.RefreshSecs != 0 {
		t.Fatalf("disabled fixed proxy retained API-only fields = %+v", cfg)
	}
}

func TestNormalizeDisabledAPIProxyPreservesConfiguration(t *testing.T) {
	cfg, err := Normalize(Config{
		APIHeaders:  []Header{{Name: " authorization ", Value: " Bearer token "}},
		APIURL:      " https://pool.example.com/next ",
		Enabled:     0,
		Host:        "ignored.example.com",
		JSONPath:    "$.data.proxy",
		Port:        8080,
		Protocol:    " socks5 ",
		RefreshSecs: 45,
		Source:      " api ",
	})
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	if cfg.Enabled != 0 || cfg.Source != SourceAPI || cfg.Protocol != ProtocolSOCKS5 || cfg.APIURL != "https://pool.example.com/next" || cfg.JSONPath != "data.proxy" || cfg.RefreshSecs != 45 {
		t.Fatalf("normalized disabled API proxy = %+v", cfg)
	}
	if len(cfg.APIHeaders) != 1 || cfg.APIHeaders[0] != (Header{Name: "Authorization", Value: "Bearer token"}) {
		t.Fatalf("normalized disabled API headers = %+v", cfg.APIHeaders)
	}
	if cfg.Host != "" || cfg.Port != 0 {
		t.Fatalf("disabled API proxy retained fixed-only fields = %+v", cfg)
	}
}

func TestNormalizeEmptyDisabledProxyRemainsEmpty(t *testing.T) {
	cfg, err := Normalize(Config{Enabled: 0})
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	if cfg.Enabled != 0 || cfg.Source != "" || cfg.Protocol != "" || cfg.Host != "" || cfg.Port != 0 || cfg.APIURL != "" || len(cfg.APIHeaders) != 0 || cfg.JSONPath != "" || cfg.RefreshSecs != 0 || cfg.Username != "" || cfg.Password != "" || cfg.PasswordConfigured {
		t.Fatalf("normalized empty disabled proxy = %+v", cfg)
	}
}

func TestNormalizeFixedProxyRequiresProtocolHostAndPort(t *testing.T) {
	cfg, err := Normalize(Config{
		Enabled:  1,
		Source:   SourceFixed,
		Protocol: ProtocolSOCKS5,
		Host:     " 203.0.113.10 ",
		Port:     1080,
		Username: " proxy-user ",
		Password: " proxy-pass ",
	})
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	if cfg.Host != "203.0.113.10" || cfg.Protocol != ProtocolSOCKS5 || cfg.Port != 1080 {
		t.Fatalf("normalized fixed proxy = %+v", cfg)
	}
	for _, invalid := range []Config{
		{Enabled: 1, Source: SourceFixed, Protocol: ProtocolHTTP, Port: 8080},
		{Enabled: 1, Source: SourceFixed, Protocol: ProtocolHTTP, Host: "proxy.example.com"},
		{Enabled: 1, Source: SourceFixed, Protocol: "FTP", Host: "proxy.example.com", Port: 21},
	} {
		if _, err := Normalize(invalid); err == nil {
			t.Fatalf("Normalize(%+v) unexpectedly succeeded", invalid)
		}
	}
}

func TestParseAPIResponseUsesConfiguredJSONPath(t *testing.T) {
	cfg, err := Normalize(Config{
		APIURL:      "https://pool.example.com/next?token=abc",
		Enabled:     1,
		JSONPath:    "result.node",
		Protocol:    ProtocolHTTPS,
		Source:      SourceAPI,
		APIHeaders:  []Header{{Name: "Authorization", Value: "Bearer token"}},
		RefreshSecs: 30,
	})
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	endpoint, candidates, err := ParseAPIResponse(cfg, []byte(`{"result":{"node":"198.51.100.8:8443"}}`))
	if err != nil {
		t.Fatalf("ParseAPIResponse() error = %v", err)
	}
	if endpoint.Protocol != ProtocolHTTPS || endpoint.Host != "198.51.100.8" || endpoint.Port != 8443 {
		t.Fatalf("endpoint = %+v", endpoint)
	}
	if len(candidates) != 1 || candidates[0].Path != "result.node" {
		t.Fatalf("candidates = %+v", candidates)
	}
}

func TestParseAPIResponseAutomaticallyDetectsCombinedAndSplitProxyFields(t *testing.T) {
	for _, test := range []struct {
		body string
		host string
		port int
		path string
	}{
		{`{"data":{"proxy":"192.0.2.20:3128"}}`, "192.0.2.20", 3128, "data.proxy"},
		{`{"data":{"ip":"192.0.2.21","port":8080}}`, "192.0.2.21", 8080, "data.ip"},
		{`{"items":[{"host":"192.0.2.22","port":"1080"}]}`, "192.0.2.22", 1080, "items.0.host"},
	} {
		cfg, err := Normalize(Config{APIURL: "https://pool.example.com/next", Enabled: 1, Protocol: ProtocolHTTP, Source: SourceAPI})
		if err != nil {
			t.Fatal(err)
		}
		endpoint, candidates, err := ParseAPIResponse(cfg, []byte(test.body))
		if err != nil {
			t.Fatalf("ParseAPIResponse(%s) error = %v", test.body, err)
		}
		if endpoint.Host != test.host || endpoint.Port != test.port {
			t.Fatalf("ParseAPIResponse(%s) = %+v", test.body, endpoint)
		}
		found := false
		for _, candidate := range candidates {
			if candidate.Path == test.path {
				found = true
			}
		}
		if !found {
			t.Fatalf("candidate %q not found in %+v", test.path, candidates)
		}
	}
}

func TestParseAPIResponseRejectsMissingProxy(t *testing.T) {
	cfg, err := Normalize(Config{APIURL: "https://pool.example.com/next", Enabled: 1, Protocol: ProtocolHTTP, Source: SourceAPI})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := ParseAPIResponse(cfg, []byte(`{"code":200,"message":"ok"}`)); err == nil {
		t.Fatal("response without a proxy was accepted")
	}
}
