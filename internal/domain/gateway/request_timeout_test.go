package gateway

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestInvokeUpstreamsHonorsEffectiveInterfaceTimeoutAcrossUpstreamCall(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		time.Sleep(300 * time.Millisecond)
	}))
	defer server.Close()

	service := &Service{httpClient: server.Client()}
	ctx := context.Background()
	started := time.Now()
	_, err := service.invokeUpstreams(ctx, InterfaceConfig{
		RequestTimeoutMs: 100,
		RequestURL:       server.URL,
	}, http.MethodGet, OpenRequest{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("invokeUpstreams() error = %v, want context deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > 260*time.Millisecond {
		t.Fatalf("configured timeout did not stop the request promptly: %s", elapsed)
	}
}
