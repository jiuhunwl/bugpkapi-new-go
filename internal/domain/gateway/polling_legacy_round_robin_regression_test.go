package gateway

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/redis/go-redis/v9"
)

// equalHealthRoundRobinHook supplies two equally ranked candidates and an
// in-memory Lua counter so round-robin behavior is deterministic without a
// Redis process.
type equalHealthRoundRobinHook struct {
	mu      sync.Mutex
	counter int64
}

func (h *equalHealthRoundRobinHook) DialHook(next redis.DialHook) redis.DialHook {
	return next
}

func (h *equalHealthRoundRobinHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		switch strings.ToLower(cmd.Name()) {
		case "eval", "evalsha":
			value, ok := cmd.(*redis.Cmd)
			if !ok {
				return fmt.Errorf("round-robin counter command has type %T", cmd)
			}
			h.mu.Lock()
			h.counter++
			current := h.counter
			h.mu.Unlock()
			value.SetVal(current)
			return nil
		default:
			return next(ctx, cmd)
		}
	}
}

func (h *equalHealthRoundRobinHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(_ context.Context, cmds []redis.Cmder) error {
		for _, cmd := range cmds {
			if !strings.EqualFold(cmd.Name(), "hgetall") {
				continue
			}
			value, ok := cmd.(*redis.MapStringStringCmd)
			if !ok {
				return fmt.Errorf("health command has type %T", cmd)
			}
			value.SetVal(map[string]string{})
		}
		return nil
	}
}

func TestInvokeUpstreamsRotatesLegacyEmptyModeRoundRobinPool(t *testing.T) {
	var calls []string
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})
	client.AddHook(&equalHealthRoundRobinHook{})
	t.Cleanup(func() { _ = client.Close() })

	service := &Service{
		rdb: client,
		httpClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			calls = append(calls, request.URL.Host)
			return &http.Response{
				Body:       io.NopCloser(strings.NewReader(`{"code":200,"data":{"ok":true}}`)),
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Request:    request,
				StatusCode: http.StatusOK,
			}, nil
		})},
		healthRecordFn: func(context.Context, int64, string, bool, int64) error { return nil },
	}
	api := InterfaceConfig{
		ID:             1401,
		PollingEnabled: 0,
		PollingMode:    "",
		RequestURL:     "https://legacy-primary.example/must-not-call",
		UpstreamConfigs: []UpstreamConfig{
			{URL: "https://node-a.example/api"},
			{URL: "https://node-b.example/api"},
		},
	}

	first, err := service.invokeUpstreams(context.Background(), api, http.MethodGet, OpenRequest{})
	if err != nil {
		t.Fatalf("first legacy round-robin invoke failed: %v", err)
	}
	second, err := service.invokeUpstreams(context.Background(), api, http.MethodGet, OpenRequest{})
	if err != nil {
		t.Fatalf("second legacy round-robin invoke failed: %v", err)
	}

	if len(calls) != 2 || calls[0] != "node-a.example" || calls[1] != "node-b.example" {
		t.Fatalf("legacy empty-mode pool did not rotate across calls: calls=%v", calls)
	}
	if first.PollingMode != "ROUND_ROBIN" || second.PollingMode != "ROUND_ROBIN" {
		t.Fatalf("runtime modes = %q, %q; want ROUND_ROBIN", first.PollingMode, second.PollingMode)
	}
}
