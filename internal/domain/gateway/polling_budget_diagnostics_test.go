package gateway

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestPollingLargePoolDoesNotDivideCandidateBudgetByPoolSize(t *testing.T) {
	parent, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, candidates := range []int{2, 5, 20} {
		before := time.Now()
		child, stop := pollingAttemptContext(parent, candidates, true)
		deadline, _ := child.Deadline()
		remaining := deadline.Sub(before)
		stop()
		if remaining < 4500*time.Millisecond || remaining > 5100*time.Millisecond {
			t.Fatalf("%d candidates reduced a healthy node's budget to %s; reserve half for this attempt and half for fallbacks", candidates, remaining)
		}
	}
}

func TestPollingSlowHealthyPrimaryIsNotPreemptedByUnusedBackups(t *testing.T) {
	calls := 0
	service := &Service{httpClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		if calls != 1 {
			return nil, errors.New("backup unavailable")
		}
		select {
		case <-time.After(350 * time.Millisecond):
			return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"code":200}`)), Request: request}, nil
		case <-request.Context().Done():
			return nil, request.Context().Err()
		}
	})}}
	result, err := service.invokeUpstreams(context.Background(), InterfaceConfig{RequestTimeoutMs: 1200, PollingEnabled: 1, PollingMode: "PRIMARY", RequestURL: "https://primary.example/api", UpstreamConfigs: []UpstreamConfig{{URL: "https://one.example/api"}, {URL: "https://two.example/api"}, {URL: "https://three.example/api"}, {URL: "https://four.example/api"}}}, http.MethodGet, OpenRequest{})
	if err != nil || !result.Success || calls != 1 {
		t.Fatalf("healthy primary should get time to respond before fallback: success=%v calls=%d error=%v", result.Success, calls, err)
	}
}

func TestPollingMixedFailuresExplainNodeAndFailureType(t *testing.T) {
	calls := 0
	service := &Service{httpClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return nil, context.DeadlineExceeded
		}
		return &http.Response{StatusCode: 503, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("temporarily unavailable")), Request: request}, nil
	})}}
	result, err := service.invokeUpstreams(context.Background(), InterfaceConfig{RequestTimeoutMs: 1000, PollingEnabled: 1, PollingMode: "PRIMARY", RequestURL: "https://primary.example/private", UpstreamConfigs: []UpstreamConfig{{URL: "https://backup.example/private"}}}, http.MethodGet, OpenRequest{})
	if err == nil || result.Success {
		t.Fatalf("failed nodes must not become billable successes: %+v %v", result, err)
	}
	for _, text := range []string{"节点1", "上游响应超时", "节点2", "HTTP 503"} {
		if !strings.Contains(err.Error(), text) {
			t.Fatalf("failure missing %q: %v", text, err)
		}
	}
	if strings.Contains(err.Error(), "context deadline exceeded") || strings.Contains(err.Error(), "primary.example") {
		t.Fatalf("raw upstream error leaked into summary: %v", err)
	}
}

func TestPollingFallbackBudgetDoesNotStarveLateCandidates(t *testing.T) {
	parent, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	before := time.Now()
	child, stop := pollingAttemptContext(parent, 5, false)
	defer stop()
	deadline, _ := child.Deadline()
	remaining := deadline.Sub(before)
	if remaining < 1900*time.Millisecond || remaining > 2100*time.Millisecond {
		t.Fatalf("fallback should use one fifth of remaining budget, got %s", remaining)
	}
}
