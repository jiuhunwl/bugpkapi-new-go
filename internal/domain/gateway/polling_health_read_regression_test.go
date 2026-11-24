package gateway

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/redis/go-redis/v9"
)

// blockingHealthReadHook holds the advisory health pipeline until its bounded
// context expires.  The test below uses it to prove that a slow Redis lookup
// cannot consume the whole interface request budget before polling starts.
type blockingHealthReadHook struct{}

func (blockingHealthReadHook) DialHook(next redis.DialHook) redis.DialHook { return next }

func (blockingHealthReadHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		return next(ctx, cmd)
	}
}

func (blockingHealthReadHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
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

func TestInvokeUpstreamsStartsPollingWhenHealthReadIsSlow(t *testing.T) {
	var calls []string
	service := &Service{
		httpClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
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
	rdb.AddHook(blockingHealthReadHook{})
	service.rdb = rdb
	t.Cleanup(func() { _ = rdb.Close() })

	// 100ms is the minimum accepted interface timeout.  A health lookup must
	// receive only one fair share (about 33ms for two candidates), leaving time
	// for the primary and backup attempts.  A fixed 75ms lookup performed twice
	// would leave no budget for the backup and reproduce the intermittent bug.
	result, err := service.invokeUpstreams(context.Background(), InterfaceConfig{
		ID:               998,
		RequestTimeoutMs: 100,
		PollingEnabled:   1,
		PollingMode:      "PRIMARY",
		RequestURL:       "https://primary.example/api",
		UpstreamConfigs:  []UpstreamConfig{{URL: "https://backup.example/api"}},
	}, http.MethodGet, OpenRequest{})
	if err != nil {
		t.Fatalf("slow health read must not suppress backup polling: %v", err)
	}
	if len(calls) != 2 || !strings.Contains(calls[0], "primary.example") || !strings.Contains(calls[1], "backup.example") {
		t.Fatalf("expected primary then backup calls, got %v", calls)
	}
	if !result.Success || !result.UpstreamSwitched {
		t.Fatalf("unexpected fallback result: %+v", result)
	}
}
