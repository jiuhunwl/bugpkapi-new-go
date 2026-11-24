package gateway

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// responseCacheSetSpy lets this regression test exercise storeResponseCache
// without requiring a Redis process.  The production write is intercepted
// only after the polling-policy guard has run.
type responseCacheSetSpy struct {
	sets int
}

func (s *responseCacheSetSpy) DialHook(next redis.DialHook) redis.DialHook { return next }

func (s *responseCacheSetSpy) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		if strings.EqualFold(cmd.Name(), "set") {
			s.sets++
			return nil
		}
		return next(ctx, cmd)
	}
}

func (s *responseCacheSetSpy) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}

func TestStoreResponseCacheDoesNotPersistNonTerminalBusinessCode(t *testing.T) {
	now := time.Date(2026, time.August, 10, 0, 30, 0, 0, time.UTC)
	spy := &responseCacheSetSpy{}
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})
	client.AddHook(spy)
	t.Cleanup(func() { _ = client.Close() })

	service := &Service{rdb: client, now: func() time.Time { return now }}
	api := InterfaceConfig{
		ID:                     9910,
		RequestURL:             "https://primary.example/api",
		CacheEnabled:           1,
		CacheDefaultTTLSeconds: 60,
		PollingEnabled:         1,
		PollingMode:            "PRIMARY",
		PollingCheckEnabled:    0,
		UpstreamConfigs:        []UpstreamConfig{{URL: "https://backup.example/api"}},
	}
	result := OpenResult{
		Body:           `{"code":500,"message":"temporary"}`,
		ContentType:    "application/json",
		ResponseStatus: 200,
		Success:        true, // emulate a stale/legacy caller result
	}
	service.storeResponseCache(context.Background(), api, "GET", OpenRequest{ClientIP: "203.0.113.910"}, &result)
	if spy.sets != 0 || result.cacheStored {
		t.Fatalf("non-terminal business response was persisted: sets=%d result=%+v", spy.sets, result)
	}
}
