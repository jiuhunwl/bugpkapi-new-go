package interfaceapi

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/redis/go-redis/v9"
)

// blockingInterfaceHealthReadHook holds the advisory health pipeline until
// its context expires.  A FASTEST invoke must use a bounded child context for
// this lookup; otherwise a slow Redis read consumes the complete request
// deadline before the first upstream candidate is attempted.
type blockingInterfaceHealthReadHook struct{}

func (blockingInterfaceHealthReadHook) DialHook(next redis.DialHook) redis.DialHook {
	return next
}

func (blockingInterfaceHealthReadHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		return next(ctx, cmd)
	}
}

func (blockingInterfaceHealthReadHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []redis.Cmder) error {
		for _, cmd := range cmds {
			if strings.EqualFold(cmd.Name(), "hgetall") {
				<-ctx.Done()
				return ctx.Err()
			}
		}
		return next(ctx, cmds)
	}
}

func TestInvokeUpstreamsFastestHealthReadDoesNotConsumeRequestDeadline(t *testing.T) {
	var calls []string
	service := &Service{
		httpClient: &http.Client{Transport: interfaceRoundTripFunc(func(request *http.Request) (*http.Response, error) {
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
		})},
		healthRecordFn: func(context.Context, int64, string, bool, int64) error { return nil },
	}
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})
	rdb.AddHook(blockingInterfaceHealthReadHook{})
	service.rdb = rdb
	t.Cleanup(func() { _ = rdb.Close() })

	// A slow health read receives only a fair metadata slice.  The remaining
	// budget must still be available for both the failed primary and successful
	// backup attempts.
	result, err := service.invokeUpstreams(context.Background(), InterfaceItem{
		ID:               "999",
		RequestTimeoutMs: 200,
		PollingEnabled:   1,
		PollingMode:      "FASTEST",
		RequestURL:       "https://primary.example/api",
		UpstreamConfigs:  []UpstreamConfig{{URL: "https://backup.example/api"}},
	}, http.MethodGet, InvokePayload{})
	if err != nil {
		t.Fatalf("slow health read must not suppress FASTEST fallback: %v", err)
	}
	if len(calls) != 2 || !strings.Contains(calls[0], "primary.example") || !strings.Contains(calls[1], "backup.example") {
		t.Fatalf("expected primary then backup calls, got %v", calls)
	}
	if !result.Success || !result.UpstreamSwitched {
		t.Fatalf("unexpected fallback result: %+v", result)
	}
}

// blockingInterfaceRoundRobinCounterHook holds the Redis Lua counter used to
// choose the starting node.  The counter is advisory; a slow Redis operation
// must not consume the entire invoke deadline and prevent the polling chain.
type blockingInterfaceRoundRobinCounterHook struct{}

func (blockingInterfaceRoundRobinCounterHook) DialHook(next redis.DialHook) redis.DialHook {
	return next
}

func (blockingInterfaceRoundRobinCounterHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		name := strings.ToLower(cmd.Name())
		if name == "eval" || name == "evalsha" {
			<-ctx.Done()
			return ctx.Err()
		}
		return next(ctx, cmd)
	}
}

func (blockingInterfaceRoundRobinCounterHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}

func TestInvokeUpstreamsRoundRobinCounterDoesNotConsumeRequestDeadline(t *testing.T) {
	var calls []string
	service := &Service{
		httpClient: &http.Client{Transport: interfaceRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			calls = append(calls, request.URL.String())
			body := `{"code":500,"message":"temporary"}`
			if strings.Contains(request.URL.Host, "second") {
				body = `{"code":200,"data":{"ok":true}}`
			}
			return &http.Response{
				Body:       io.NopCloser(strings.NewReader(body)),
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Request:    request,
				StatusCode: http.StatusOK,
			}, nil
		})},
		healthRecordFn: func(context.Context, int64, string, bool, int64) error { return nil },
	}
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})
	rdb.AddHook(blockingInterfaceRoundRobinCounterHook{})
	service.rdb = rdb
	t.Cleanup(func() { _ = rdb.Close() })

	result, err := service.invokeUpstreams(context.Background(), InterfaceItem{
		ID:               "1000",
		RequestTimeoutMs: 200,
		PollingEnabled:   1,
		PollingMode:      "ROUND_ROBIN",
		UpstreamConfigs: []UpstreamConfig{
			{URL: "https://first.example/api"},
			{URL: "https://second.example/api"},
		},
	}, http.MethodGet, InvokePayload{})
	if err != nil {
		t.Fatalf("slow round-robin counter must not suppress fallback: %v", err)
	}
	if len(calls) != 2 || !strings.Contains(calls[0], "first.example") || !strings.Contains(calls[1], "second.example") {
		t.Fatalf("expected first then second calls, got %v", calls)
	}
	if !result.Success || !result.UpstreamSwitched {
		t.Fatalf("unexpected fallback result: %+v", result)
	}
}
