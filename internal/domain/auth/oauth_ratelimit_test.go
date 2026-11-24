package auth

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestOAuthRegisterIPDailyLimit(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	limiter := NewRateLimiter(rdb, "oauth-rate-limit-test-secret")
	ctx := context.Background()
	if err := limiter.CheckRegisterChannel(ctx, "oauth:test", "203.0.113.10", 0, 1); err != nil {
		t.Fatalf("first registration rejected: %v", err)
	}
	if err := limiter.CheckRegisterChannel(ctx, "oauth:test", "203.0.113.10", 0, 1); err == nil {
		t.Fatal("second registration should hit the daily limit")
	}
	if err := limiter.CheckRegisterChannel(ctx, "oauth:test", "203.0.113.10", 0, 0); err != nil {
		t.Fatalf("disabled limit rejected: %v", err)
	}
}
