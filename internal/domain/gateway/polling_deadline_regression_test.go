package gateway

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

// A node that consumes its fair share of the interface budget must not prevent
// the next node from being attempted.  The old implementation passed one
// shared context to every invokeOne call; once the first node reached the
// overall deadline, the loop returned before it could poll the backup.
func TestInvokeUpstreamsSplitsRemainingDeadlineAcrossCandidates(t *testing.T) {
	var calls []string
	service := &Service{httpClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
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
	})}}

	result, err := service.invokeUpstreams(context.Background(), InterfaceConfig{
		ID:               993,
		RequestTimeoutMs: 240,
		PollingEnabled:   1,
		PollingMode:      "PRIMARY",
		RequestURL:       "https://primary.example/api",
		UpstreamConfigs:  []UpstreamConfig{{URL: "https://backup.example/api"}},
	}, http.MethodGet, OpenRequest{})

	if err != nil {
		t.Fatalf("backup should succeed after primary slice timeout: %v", err)
	}
	if len(calls) != 2 || !strings.Contains(calls[0], "primary.example") || !strings.Contains(calls[1], "backup.example") {
		t.Fatalf("expected primary then backup calls, got %v", calls)
	}
	if !result.Success || !result.UpstreamSwitched {
		t.Fatalf("unexpected fallback result: %+v", result)
	}
}
