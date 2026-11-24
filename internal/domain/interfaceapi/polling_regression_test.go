package interfaceapi

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestUpstreamCandidatesNormalizeHistoricalPollingMode(t *testing.T) {
	backup := UpstreamConfig{URL: "https://backup.example.com/api"}

	primary := upstreamCandidates(InterfaceItem{
		PollingEnabled:  1,
		PollingMode:     "  primary  ",
		RequestURL:      "https://primary.example.com/api",
		UpstreamConfigs: []UpstreamConfig{backup},
	})
	if len(primary) != 2 || primary[0].URL != "https://primary.example.com/api" || primary[1].URL != backup.URL {
		t.Fatalf("trimmed/lowercase PRIMARY candidates = %+v", primary)
	}

	legacyEmpty := upstreamCandidates(InterfaceItem{
		PollingEnabled:  1,
		PollingMode:     "",
		RequestURL:      "https://primary.example.com/api",
		UpstreamConfigs: []UpstreamConfig{backup},
	})
	if len(legacyEmpty) != 1 || legacyEmpty[0].URL != backup.URL {
		t.Fatalf("empty enabled mode must use ROUND_ROBIN semantics, candidates = %+v", legacyEmpty)
	}
}

func TestInvokeUpstreamsReportsNormalizedRuntimePollingMode(t *testing.T) {
	service := &Service{httpClient: &http.Client{Transport: interfaceRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			Body:       io.NopCloser(strings.NewReader(`{"code":200}`)),
			Header:     make(http.Header),
			Request:    request,
			StatusCode: http.StatusOK,
		}, nil
	})}}

	result, err := service.invokeUpstreams(context.Background(), InterfaceItem{
		ID:              "104",
		PollingEnabled:  1,
		PollingMode:     "  round_robin  ",
		UpstreamConfigs: []UpstreamConfig{{URL: "https://first.example/api"}},
	}, http.MethodGet, InvokePayload{})
	if err != nil {
		t.Fatalf("normalized runtime mode returned error: %v", err)
	}
	if result.PollingMode != "ROUND_ROBIN" {
		t.Fatalf("result polling mode = %q, want ROUND_ROBIN", result.PollingMode)
	}
}

// TestInvokeUpstreamsRetriesNonStopBusinessCodeAcrossNodes documents the
// contract that a configured stop code is the only business response that may
// short-circuit polling.  A per-node check toggle of zero means "inherit" the
// interface-level check; it must not turn an arbitrary non-404 code into a
// successful response.
func TestInvokeUpstreamsRetriesNonStopBusinessCodeAcrossNodes(t *testing.T) {
	var calls []string
	service := &Service{httpClient: &http.Client{Transport: interfaceRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls = append(calls, request.URL.String())
		return &http.Response{
			Body:       io.NopCloser(strings.NewReader(`{"code":500,"message":"temporary failure"}`)),
			Header:     make(http.Header),
			Request:    request,
			StatusCode: http.StatusOK,
		}, nil
	})}}

	_, err := service.invokeUpstreams(context.Background(), InterfaceItem{
		ID:                   "101",
		PollingEnabled:       1,
		PollingMode:          "PRIMARY",
		RequestURL:           "https://primary.example/api",
		PollingCheckEnabled:  1,
		PollingCheckField:    "code",
		PollingCheckExpected: "200",
		PollingStopCode:      "404",
		// The backup has no per-node override. It must inherit the global rule.
		UpstreamConfigs: []UpstreamConfig{{URL: "https://backup.example/api"}},
	}, http.MethodGet, InvokePayload{})

	if err == nil {
		t.Fatalf("non-stop business code was accepted as success: calls=%v", calls)
	}
	if len(calls) != 2 {
		t.Fatalf("non-stop business code must exhaust both nodes, calls=%v", calls)
	}
}

// The same inheritance rule applies when no custom stop code is configured:
// the global response check still decides whether a 2xx payload is valid.
func TestInvokeUpstreamsInheritsGlobalCheckForEveryPrimaryFallbackNode(t *testing.T) {
	var calls []string
	service := &Service{httpClient: &http.Client{Transport: interfaceRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls = append(calls, request.URL.String())
		body := `{"code":500}`
		if strings.Contains(request.URL.Host, "backup-2") {
			body = `{"code":200}`
		}
		return &http.Response{
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     make(http.Header),
			Request:    request,
			StatusCode: http.StatusOK,
		}, nil
	})}}

	result, err := service.invokeUpstreams(context.Background(), InterfaceItem{
		ID:                   "104",
		PollingEnabled:       1,
		PollingMode:          "PRIMARY",
		RequestURL:           "https://primary.example/api",
		PollingCheckEnabled:  1,
		PollingCheckField:    "code",
		PollingCheckExpected: "200",
		UpstreamConfigs: []UpstreamConfig{
			{URL: "https://backup-1.example/api"},
			{URL: "https://backup-2.example/api"},
		},
	}, http.MethodGet, InvokePayload{})

	if err != nil {
		t.Fatalf("the valid final node should succeed: %v", err)
	}
	if len(calls) != 3 {
		t.Fatalf("global check must reject both non-success payloads before backup-2, calls=%v", calls)
	}
	if !result.Success || !result.UpstreamSwitched || !strings.Contains(result.UpstreamURL, "backup-2.example") {
		t.Fatalf("unexpected inherited-check result: %+v", result)
	}
}

// Legacy polling rows may have response checking disabled and no custom stop
// code.  When an upstream still returns a standard business `code`, a
// non-success/non-404 value must be treated as a node failure and the next
// upstream must be attempted.
func TestInvokeUpstreamsRetriesNon404BusinessCodeWithoutExplicitCheck(t *testing.T) {
	var calls []string
	service := &Service{httpClient: &http.Client{Transport: interfaceRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls = append(calls, request.URL.String())
		body := `{"code":500,"message":"temporary"}`
		if strings.Contains(request.URL.Host, "backup") {
			body = `{"code":200,"data":{"ok":true}}`
		}
		return &http.Response{
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Request:    request,
			StatusCode: http.StatusOK,
		}, nil
	})}}

	result, err := service.invokeUpstreams(context.Background(), InterfaceItem{
		ID:              "106",
		PollingEnabled:  1,
		PollingMode:     "PRIMARY",
		RequestURL:      "https://primary.example/api",
		UpstreamConfigs: []UpstreamConfig{{URL: "https://backup.example/api"}},
	}, http.MethodGet, InvokePayload{})
	if err != nil {
		t.Fatalf("backup success should be returned after non-404 business code: %v", err)
	}
	if len(calls) != 2 {
		t.Fatalf("non-404 business code must trigger fallback without explicit check, calls=%v", calls)
	}
	if !result.Success || !result.UpstreamSwitched || !strings.Contains(result.UpstreamURL, "backup.example") {
		t.Fatalf("unexpected fallback result: %+v", result)
	}
}

// A stop-code configuration is itself a request to inspect the business code.
// It must therefore enable the global check even for legacy rows where the
// global pollingCheckEnabled flag was persisted as zero.
func TestInvokeUpstreamsStopCodeForcesCheckWhenGlobalToggleIsDisabled(t *testing.T) {
	var calls []string
	service := &Service{httpClient: &http.Client{Transport: interfaceRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls = append(calls, request.URL.String())
		body := `{"code":500,"message":"temporary failure"}`
		if strings.Contains(request.URL.Host, "backup") {
			body = `{"code":200}`
		}
		return &http.Response{
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     make(http.Header),
			Request:    request,
			StatusCode: http.StatusOK,
		}, nil
	})}}

	result, err := service.invokeUpstreams(context.Background(), InterfaceItem{
		ID:              "105",
		PollingEnabled:  1,
		PollingMode:     "PRIMARY",
		RequestURL:      "https://primary.example/api",
		PollingStopCode: "404",
		// This mirrors legacy rows that did not enable the global check.
		PollingCheckEnabled:  0,
		PollingCheckField:    "code",
		PollingCheckExpected: "200",
		UpstreamConfigs:      []UpstreamConfig{{URL: "https://backup.example/api"}},
	}, http.MethodGet, InvokePayload{})

	if err != nil {
		t.Fatalf("backup success should be returned after non-stop primary code: %v", err)
	}
	if len(calls) != 2 {
		t.Fatalf("stop-code mode must inspect the primary business code before returning, calls=%v", calls)
	}
	if !result.Success || !result.UpstreamSwitched || !strings.Contains(result.UpstreamURL, "backup.example") {
		t.Fatalf("unexpected stop-code forced-check result: %+v", result)
	}
}

// TestInvokeUpstreamsStopsOnConfiguredBusinessCode ensures that only the
// configured 404 business code is treated as a terminal, pass-through result.
func TestInvokeUpstreamsStopsOnConfiguredBusinessCode(t *testing.T) {
	var calls []string
	service := &Service{httpClient: &http.Client{Transport: interfaceRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls = append(calls, request.URL.String())
		body := `{"code":404,"message":"not found"}`
		if strings.Contains(request.URL.Host, "backup") {
			body = `{"code":200}`
		}
		return &http.Response{
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     make(http.Header),
			Request:    request,
			StatusCode: http.StatusOK,
		}, nil
	})}}

	result, err := service.invokeUpstreams(context.Background(), InterfaceItem{
		ID:                   "102",
		PollingEnabled:       1,
		PollingMode:          "PRIMARY",
		RequestURL:           "https://primary.example/api",
		PollingCheckEnabled:  1,
		PollingCheckField:    "code",
		PollingCheckExpected: "200",
		PollingStopCode:      "404",
		UpstreamConfigs:      []UpstreamConfig{{URL: "https://backup.example/api"}},
	}, http.MethodGet, InvokePayload{})

	if err != nil {
		t.Fatalf("configured stop code should pass through without an error: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("configured stop code must stop before backup, calls=%v", calls)
	}
	if result.Success || result.StatusCode != http.StatusOK || !strings.Contains(result.Body, `"code":404`) {
		t.Fatalf("unexpected stop-code result: %+v", result)
	}
}

func TestInvokeUpstreamsChecksConfiguredStopCodeBeforeResponseCleaning(t *testing.T) {
	var calls []string
	service := &Service{httpClient: &http.Client{Transport: interfaceRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls = append(calls, request.URL.String())
		return &http.Response{
			Body:       io.NopCloser(strings.NewReader(`{"code":0,"message":"terminal"}`)),
			Header:     make(http.Header),
			Request:    request,
			StatusCode: http.StatusOK,
		}, nil
	})}}

	result, err := service.invokeUpstreams(context.Background(), InterfaceItem{
		ID:                      "106",
		PollingEnabled:          1,
		PollingMode:             "PRIMARY",
		RequestURL:              "https://primary.example/api",
		PollingCheckEnabled:     1,
		PollingCheckField:       "code",
		PollingCheckExpected:    "200",
		PollingStopCode:         "0",
		ResponseCleaningEnabled: 1,
		UpstreamConfigs:         []UpstreamConfig{{URL: "https://backup.example/api"}},
	}, http.MethodGet, InvokePayload{})

	if err != nil {
		t.Fatalf("configured stop code should terminate before cleaning: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("terminal raw code must not advance to backup, calls=%v", calls)
	}
	if result.Success || result.StatusCode != http.StatusOK || !strings.Contains(result.Body, `"code":0`) {
		t.Fatalf("raw terminal response was not preserved: %+v", result)
	}
}

// TestInvokeUpstreamsStopsOnDefaultHTTP404 keeps the documented legacy
// behavior when no custom business stop code is configured.
func TestInvokeUpstreamsStopsOnDefaultHTTP404(t *testing.T) {
	var calls []string
	service := &Service{httpClient: &http.Client{Transport: interfaceRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls = append(calls, request.URL.String())
		return &http.Response{
			Body:       io.NopCloser(strings.NewReader(`{"message":"not found"}`)),
			Header:     make(http.Header),
			Request:    request,
			StatusCode: http.StatusNotFound,
		}, nil
	})}}

	result, err := service.invokeUpstreams(context.Background(), InterfaceItem{
		ID:              "103",
		PollingEnabled:  1,
		PollingMode:     "PRIMARY",
		RequestURL:      "https://primary.example/api",
		UpstreamConfigs: []UpstreamConfig{{URL: "https://backup.example/api"}},
	}, http.MethodGet, InvokePayload{})

	if err != nil {
		t.Fatalf("default HTTP 404 should pass through without an error: %v", err)
	}
	if len(calls) != 1 || result.StatusCode != http.StatusNotFound || result.Success {
		t.Fatalf("unexpected default 404 result: calls=%v result=%+v", calls, result)
	}
}

func TestInvokeUpstreamsStopsOnDefaultBusiness404Code(t *testing.T) {
	var calls []string
	service := &Service{httpClient: &http.Client{Transport: interfaceRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls = append(calls, request.URL.String())
		return &http.Response{
			Body:       io.NopCloser(strings.NewReader(`{"code":404,"message":"not found"}`)),
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Request:    request,
			StatusCode: http.StatusOK,
		}, nil
	})}}

	result, err := service.invokeUpstreams(context.Background(), InterfaceItem{
		ID:              "107",
		PollingEnabled:  1,
		PollingMode:     "PRIMARY",
		RequestURL:      "https://primary.example/api",
		UpstreamConfigs: []UpstreamConfig{{URL: "https://backup.example/api"}},
	}, http.MethodGet, InvokePayload{})
	if err != nil {
		t.Fatalf("business 404 should terminate polling without an error: %v", err)
	}
	if len(calls) != 1 || result.StatusCode != http.StatusOK || result.Success {
		t.Fatalf("unexpected business 404 result: calls=%v result=%+v", calls, result)
	}
}
