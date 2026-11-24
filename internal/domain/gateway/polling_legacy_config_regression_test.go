package gateway

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

// Legacy rows can contain a concrete polling mode and an upstream pool while
// the enable flag was left at its zero value by an older save path.  A
// non-terminal business code must still advance to the next configured node.
func TestInvokeUpstreamsInfersPollingFromExplicitModeWhenFlagDisabled(t *testing.T) {
	var calls []string
	service := &Service{httpClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls = append(calls, request.URL.String())
		body := `{"code":-1,"message":"请求失败"}`
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

	result, err := service.invokeUpstreams(context.Background(), InterfaceConfig{
		ID:              1201,
		PollingEnabled:  0, // historical inconsistent row
		PollingMode:     "PRIMARY",
		RequestURL:      "https://primary.example/api",
		UpstreamConfigs: []UpstreamConfig{{URL: "https://backup.example/api"}},
	}, http.MethodGet, OpenRequest{})
	if err != nil {
		t.Fatalf("backup success should be returned after legacy primary failure: %v", err)
	}
	if len(calls) != 2 || !result.Success || !result.UpstreamSwitched || !strings.Contains(result.UpstreamURL, "backup.example") {
		t.Fatalf("legacy explicit mode did not poll backup: calls=%v result=%+v", calls, result)
	}
}

// A stop-code configuration is also an explicit polling contract.  Even when
// the legacy enable bit is zero, a non-stop business code must not be accepted
// as a successful first-node response.
func TestInvokeUpstreamsInfersPollingFromStopCodeWhenFlagDisabled(t *testing.T) {
	var calls []string
	service := &Service{httpClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls = append(calls, request.URL.String())
		body := `{"code":500,"message":"temporary"}`
		if strings.Contains(request.URL.Host, "node-b") {
			body = `{"code":200}`
		}
		return &http.Response{
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Request:    request,
			StatusCode: http.StatusOK,
		}, nil
	})}}

	result, err := service.invokeUpstreams(context.Background(), InterfaceConfig{
		ID:              1202,
		PollingEnabled:  0, // historical inconsistent row
		PollingStopCode: "404",
		RequestURL:      "https://primary.example/api",
		// An omitted mode on an old row normalizes to ROUND_ROBIN once the
		// presence of this upstream pool re-enables runtime polling.
		UpstreamConfigs: []UpstreamConfig{
			{URL: "https://node-a.example/api"},
			{URL: "https://node-b.example/api"},
		},
	}, http.MethodGet, OpenRequest{})
	if err != nil {
		t.Fatalf("backup success should be returned after non-stop legacy response: %v", err)
	}
	if len(calls) != 2 || !result.Success || !result.UpstreamSwitched || !strings.Contains(result.UpstreamURL, "node-b.example") {
		t.Fatalf("legacy stop-code config did not poll backup: calls=%v result=%+v", calls, result)
	}
}

// An operator who explicitly selected SINGLE while disabled must retain the
// opt-out semantics even if stale upstream rows remain in the document.
func TestInvokeUpstreamsKeepsExplicitSingleDisabledAsSingle(t *testing.T) {
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
		ID:              1203,
		PollingEnabled:  0,
		PollingMode:     "SINGLE",
		RequestURL:      "https://primary.example/api",
		UpstreamConfigs: []UpstreamConfig{{URL: "https://stale-backup.example/api"}},
	}, http.MethodGet, OpenRequest{})
	if len(calls) != 1 || !strings.Contains(calls[0], "primary.example") {
		t.Fatalf("explicit disabled SINGLE unexpectedly polled stale backup: calls=%v", calls)
	}
}
