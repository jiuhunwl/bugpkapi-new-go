package middleware

import (
	"context"
	"os"
	"testing"
	"time"

	hertzapp "github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/common/config"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"github.com/cloudwego/hertz/pkg/route"
	"github.com/redis/go-redis/v9"
)

func TestPublicContentBucketOnlyMatchesAnonymousReads(t *testing.T) {
	prefix := "/system-api"
	tests := map[string]bool{
		"/system-api/site/config":                     true,
		"/system-api/home/overview":                   true,
		"/system-api/donation/config":                 true,
		"/system-api/donation/supporters":             true,
		"/system-api/notice/user/list":                true,
		"/system-api/notice/user/42":                  true,
		"/system-api/friend-links/public/list":        true,
		"/system-api/friend-links/public/meta":        true,
		"/system-api/site/admin/config":               false,
		"/system-api/donation/admin/config":           false,
		"/system-api/donation/admin/supporters":       false,
		"/system-api/notice/admin/list":               false,
		"/system-api/friend-links/apply":              false,
		"/api/site/config":                            false,
		"/system-api/friend-links/admin/applications": false,
	}
	for path, want := range tests {
		_, got := publicContentBucket(path, prefix)
		if got != want {
			t.Fatalf("publicContentBucket(%q) matched=%v, want %v", path, got, want)
		}
	}
}

func TestPublicClientDigestIsStableAndSecretScoped(t *testing.T) {
	first := publicClientDigest("secret-a", "203.0.113.10")
	if len(first) != 32 {
		t.Fatalf("digest length=%d, want 32", len(first))
	}
	if first != publicClientDigest("secret-a", "203.0.113.10") {
		t.Fatal("digest is not stable")
	}
	if first == publicClientDigest("secret-b", "203.0.113.10") {
		t.Fatal("digest is not scoped by secret")
	}
	if first == publicClientDigest("secret-a", "203.0.113.11") {
		t.Fatal("different IPs produced the same digest")
	}
}

func TestRedisInt64(t *testing.T) {
	for _, value := range []any{int64(7), "7", []byte("7")} {
		got, ok := redisInt64(value)
		if !ok || got != 7 {
			t.Fatalf("redisInt64(%T)=(%d,%v)", value, got, ok)
		}
	}
	if _, ok := redisInt64(struct{}{}); ok {
		t.Fatal("unsupported value was accepted")
	}
}

func TestPublicContentRateLimitRedisIntegration(t *testing.T) {
	if os.Getenv("BUGPK_RUN_REDIS_INTEGRATION") != "1" {
		t.Skip("set BUGPK_RUN_REDIS_INTEGRATION=1 to run Redis integration test")
	}
	addr := os.Getenv("BUGPK_TEST_REDIS_ADDR")
	if addr == "" {
		addr = "127.0.0.1:16379"
	}
	client := redis.NewClient(&redis.Options{Addr: addr})
	t.Cleanup(func() { _ = client.Close() })
	if err := client.Ping(context.Background()).Err(); err != nil {
		t.Fatalf("ping Redis %s: %v", addr, err)
	}
	key := "public-rate:site:" + publicClientDigest("integration-secret", "127.0.0.1")
	_ = client.Del(context.Background(), key).Err()
	t.Cleanup(func() { _ = client.Del(context.Background(), key).Err() })

	engine := route.NewEngine(config.NewOptions([]config.Option{}))
	engine.Use(PublicContentRateLimit(client, "/test-api", "integration-secret", 1, time.Minute))
	engine.GET("/test-api/site/config", func(ctx context.Context, c *hertzapp.RequestContext) {
		c.String(consts.StatusOK, "ok")
	})

	first := ut.PerformRequest(engine, consts.MethodGet, "/test-api/site/config", nil)
	if first.Code != consts.StatusOK {
		t.Fatalf("first status=%d", first.Code)
	}
	second := ut.PerformRequest(engine, consts.MethodGet, "/test-api/site/config", nil)
	if second.Code != consts.StatusTooManyRequests {
		t.Fatalf("second status=%d, want 429", second.Code)
	}
	if second.Header().Get("Retry-After") == "" || second.Header().Get("RateLimit-Remaining") != "0" {
		t.Fatalf("missing rate-limit headers: %+v", second.Header())
	}
}
