package gateway

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

// Health bookkeeping is best effort and must not consume the request budget
// that is reserved for the next polling candidate.  A slow Redis write after
// the primary fails used to leave no time for the backup node.
func TestInvokeUpstreamsDoesNotLetShortHealthWriteStarveFallback(t *testing.T) {
	var calls []string
	service := &Service{
		httpClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			calls = append(calls, request.URL.String())
			if strings.Contains(request.URL.Host, "primary") {
				<-request.Context().Done()
				return nil, request.Context().Err()
			}
			return &http.Response{
				Body:       io.NopCloser(strings.NewReader(`{"code":200,"data":{"ok":true}}`)),
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Request:    request,
				StatusCode: http.StatusOK,
			}, nil
		})},
		healthRecordFn: func(ctx context.Context, _ int64, _ string, _ bool, _ int64) error {
			<-ctx.Done()
			return ctx.Err()
		},
	}

	result, err := service.invokeUpstreams(context.Background(), InterfaceConfig{
		ID:               996,
		RequestTimeoutMs: 180,
		PollingEnabled:   1,
		PollingMode:      "PRIMARY",
		RequestURL:       "https://primary.example/api",
		UpstreamConfigs:  []UpstreamConfig{{URL: "https://backup.example/api"}},
	}, http.MethodGet, OpenRequest{})

	if err != nil {
		t.Fatalf("slow health bookkeeping must not block backup success: %v", err)
	}
	if len(calls) != 2 || !strings.Contains(calls[1], "backup.example") {
		t.Fatalf("expected primary then backup calls, got %v", calls)
	}
	if !result.Success || !result.UpstreamSwitched {
		t.Fatalf("unexpected fallback result: %+v", result)
	}
}
