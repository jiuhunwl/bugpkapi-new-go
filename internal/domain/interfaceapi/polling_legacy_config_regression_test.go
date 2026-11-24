package interfaceapi

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestInvokeUpstreamsInfersPollingFromExplicitModeWhenFlagDisabled(t *testing.T) {
	var calls []string
	service := &Service{httpClient: &http.Client{Transport: interfaceRoundTripFunc(func(request *http.Request) (*http.Response, error) {
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

	result, err := service.invokeUpstreams(context.Background(), InterfaceItem{
		ID:              "1201",
		PollingEnabled:  0,
		PollingMode:     "PRIMARY",
		RequestURL:      "https://primary.example/api",
		UpstreamConfigs: []UpstreamConfig{{URL: "https://backup.example/api"}},
	}, http.MethodGet, InvokePayload{})
	if err != nil {
		t.Fatalf("backup success should be returned after legacy primary failure: %v", err)
	}
	if len(calls) != 2 || !result.Success || !result.UpstreamSwitched || !strings.Contains(result.UpstreamURL, "backup.example") {
		t.Fatalf("legacy explicit mode did not poll backup: calls=%v result=%+v", calls, result)
	}
}

func TestInvokeUpstreamsInfersPollingFromStopCodeWhenFlagDisabled(t *testing.T) {
	var calls []string
	service := &Service{httpClient: &http.Client{Transport: interfaceRoundTripFunc(func(request *http.Request) (*http.Response, error) {
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

	result, err := service.invokeUpstreams(context.Background(), InterfaceItem{
		ID:              "1202",
		PollingEnabled:  0,
		PollingStopCode: "404",
		RequestURL:      "https://primary.example/api",
		UpstreamConfigs: []UpstreamConfig{
			{URL: "https://node-a.example/api"},
			{URL: "https://node-b.example/api"},
		},
	}, http.MethodGet, InvokePayload{})
	if err != nil {
		t.Fatalf("backup success should be returned after non-stop legacy response: %v", err)
	}
	if len(calls) != 2 || !result.Success || !result.UpstreamSwitched || !strings.Contains(result.UpstreamURL, "node-b.example") {
		t.Fatalf("legacy stop-code config did not poll backup: calls=%v result=%+v", calls, result)
	}
}

func TestInvokeUpstreamsKeepsExplicitSingleDisabledAsSingle(t *testing.T) {
	var calls []string
	service := &Service{httpClient: &http.Client{Transport: interfaceRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls = append(calls, request.URL.String())
		return &http.Response{
			Body:       io.NopCloser(strings.NewReader(`{"code":500,"message":"temporary"}`)),
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Request:    request,
			StatusCode: http.StatusOK,
		}, nil
	})}}

	_, _ = service.invokeUpstreams(context.Background(), InterfaceItem{
		ID:              "1203",
		PollingEnabled:  0,
		PollingMode:     "SINGLE",
		RequestURL:      "https://primary.example/api",
		UpstreamConfigs: []UpstreamConfig{{URL: "https://stale-backup.example/api"}},
	}, http.MethodGet, InvokePayload{})
	if len(calls) != 1 || !strings.Contains(calls[0], "primary.example") {
		t.Fatalf("explicit disabled SINGLE unexpectedly polled stale backup: calls=%v", calls)
	}
}
