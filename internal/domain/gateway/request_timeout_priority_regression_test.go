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

func TestRequestTimeoutPriorityReachesUpstreamDeadline(t *testing.T) {
	tests := []struct {
		name                          string
		local, global, override, want int
	}{
		{"larger local is not globally clamped", 120000, 100, 1, 120000},
		{"smaller local takes priority", 100, 120000, 1, 100},
		{"disabled local inherits", 120000, 45000, 0, 45000},
		{"zero enabled local inherits", 0, 45000, 1, 45000},
		{"legacy missing global defaults", 0, 0, 0, 15000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			effective := resolveDeliveryPolicy(
				deliveryPolicySnapshot{RequestTimeoutMs: tt.local, RequestTimeoutOverride: tt.override},
				deliveryPolicySnapshot{RequestTimeoutMs: tt.global}, 0, 0,
			)
			var deadline, observedAt time.Time
			service := &Service{httpClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				deadline, _ = request.Context().Deadline()
				observedAt = time.Now()
				return &http.Response{
					Body:       io.NopCloser(strings.NewReader(`{"code":200}`)),
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Request:    request,
					StatusCode: http.StatusOK,
				}, nil
			})}}
			started := time.Now()
			result, err := service.invokeUpstreams(context.Background(), InterfaceConfig{
				RequestTimeoutMs: effective.RequestTimeoutMs,
				RequestURL:       "https://timeout.example/api",
			}, http.MethodGet, OpenRequest{})
			if err != nil || !result.Success {
				t.Fatalf("request failed: result=%+v, error=%v", result, err)
			}
			budget := time.Duration(tt.want) * time.Millisecond
			// Bracket creation of the actual context, without sleeping for long
			// policies or relying on a scheduler-sensitive elapsed-time threshold.
			if deadline.Before(started.Add(budget)) || deadline.After(observedAt.Add(budget)) {
				t.Fatalf("upstream deadline %v does not reflect %dms policy", deadline, tt.want)
			}
		})
	}
}

func TestRequestTimeoutPriorityDoesNotExtendCallerDeadline(t *testing.T) {
	parent, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	parentDeadline, _ := parent.Deadline()
	var got time.Time
	service := &Service{httpClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		got, _ = request.Context().Deadline()
		return &http.Response{
			Body:       io.NopCloser(strings.NewReader(`{"code":200}`)),
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Request:    request,
			StatusCode: http.StatusOK,
		}, nil
	})}}
	_, err := service.invokeUpstreams(parent, InterfaceConfig{
		RequestTimeoutMs: 120000,
		RequestURL:       "https://timeout.example/api",
	}, http.MethodGet, OpenRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(parentDeadline) {
		t.Fatalf("local policy extended caller deadline: got %v, want %v", got, parentDeadline)
	}
}

func TestRequestTimeoutPriorityPollingUsesOneFinalBudget(t *testing.T) {
	effective := resolveDeliveryPolicy(
		deliveryPolicySnapshot{RequestTimeoutMs: 600, RequestTimeoutOverride: 1},
		deliveryPolicySnapshot{RequestTimeoutMs: 100}, 0, 0,
	)
	var calls []string
	var deadlines []time.Time
	var firstObservedAt time.Time
	service := &Service{httpClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if len(calls) == 0 {
			firstObservedAt = time.Now()
		}
		calls = append(calls, request.URL.Host)
		deadline, _ := request.Context().Deadline()
		deadlines = append(deadlines, deadline)
		if len(calls) < 3 {
			<-request.Context().Done()
			return nil, request.Context().Err()
		}
		return &http.Response{
			Body:       io.NopCloser(strings.NewReader(`{"code":200,"data":"backup"}`)),
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Request:    request,
			StatusCode: http.StatusOK,
		}, nil
	})}}
	result, err := service.invokeUpstreams(context.Background(), InterfaceConfig{
		RequestTimeoutMs: effective.RequestTimeoutMs,
		PollingEnabled:   1,
		PollingMode:      "PRIMARY",
		RequestURL:       "https://primary.example/api",
		UpstreamConfigs: []UpstreamConfig{
			{URL: "https://second.example/api"},
			{URL: "https://last.example/api"},
		},
	}, http.MethodGet, OpenRequest{})
	if err != nil || !result.Success || !result.UpstreamSwitched {
		t.Fatalf("last backup should succeed within the shared budget: result=%+v, error=%v", result, err)
	}
	if strings.Join(calls, ",") != "primary.example,second.example,last.example" {
		t.Fatalf("unexpected polling sequence: %v", calls)
	}
	for i, deadline := range deadlines {
		if deadline.IsZero() || deadline.After(firstObservedAt.Add(600*time.Millisecond)) {
			t.Fatalf("candidate %d received a renewed request budget: %v", i+1, deadline)
		}
	}
}

func TestRequestTimeoutPriorityPollingReturnsOverallDeadlineExceeded(t *testing.T) {
	calls := 0
	var firstObservedAt time.Time
	var lastDeadline time.Time
	service := &Service{httpClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			firstObservedAt = time.Now()
		}
		lastDeadline, _ = request.Context().Deadline()
		<-request.Context().Done()
		return nil, request.Context().Err()
	})}}
	result, err := service.invokeUpstreams(context.Background(), InterfaceConfig{
		RequestTimeoutMs: 200,
		PollingEnabled:   1,
		PollingMode:      "PRIMARY",
		RequestURL:       "https://primary.example/api",
		UpstreamConfigs:  []UpstreamConfig{{URL: "https://backup.example/api"}},
	}, http.MethodGet, OpenRequest{})
	if !errors.Is(err, context.DeadlineExceeded) || result.Success {
		t.Fatalf("exhausted request budget must fail with its deadline: result=%+v, error=%v", result, err)
	}
	if calls != 2 {
		t.Fatalf("both candidates should get a slice of the request budget, got %d calls", calls)
	}
	if lastDeadline.After(firstObservedAt.Add(200 * time.Millisecond)) {
		t.Fatalf("backup deadline renewed the overall request budget: %v", lastDeadline)
	}
}
