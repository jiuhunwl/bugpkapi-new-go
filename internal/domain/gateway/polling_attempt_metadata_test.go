package gateway

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

// A successful fallback must retain the complete attempt list.  Without this
// metadata a call that did retry is indistinguishable from a primary-only call
// in diagnostics, which made the intermittent polling report impossible to
// verify from the call log.
func TestInvokeUpstreamsRecordsTriedUpstreamsOnFallbackSuccess(t *testing.T) {
	var calls []string
	service := &Service{httpClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
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

	result, err := service.invokeUpstreams(context.Background(), InterfaceConfig{
		ID:                   991,
		PollingEnabled:       1,
		PollingMode:          "PRIMARY",
		RequestURL:           "https://primary.example/api",
		PollingCheckEnabled:  1,
		PollingCheckField:    "code",
		PollingCheckExpected: "200",
		PollingStopCode:      "404",
		UpstreamConfigs:      []UpstreamConfig{{URL: "https://backup.example/api"}},
	}, http.MethodGet, OpenRequest{})
	if err != nil {
		t.Fatalf("fallback success returned error: %v", err)
	}
	if !result.Success || len(calls) != 2 {
		t.Fatalf("unexpected fallback result: calls=%v result=%+v", calls, result)
	}
	if len(result.TriedUpstreams) != 2 || result.TriedUpstreams[0] != calls[0] || result.TriedUpstreams[1] != calls[1] {
		t.Fatalf("successful fallback lost attempt metadata: tried=%v calls=%v", result.TriedUpstreams, calls)
	}
}
