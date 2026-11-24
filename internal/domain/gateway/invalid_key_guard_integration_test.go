package gateway

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	apperrors "bugpk-api/internal/pkg/errors"

	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"github.com/redis/go-redis/v9"
)

func TestInvalidKeyGuardRedisIntegration(t *testing.T) {
	if os.Getenv("BUGPK_GATEWAY_REDIS_INTEGRATION") != "1" {
		t.Skip("set BUGPK_GATEWAY_REDIS_INTEGRATION=1 to run Redis integration")
	}
	address := os.Getenv("REDIS_ADDR")
	if address == "" {
		address = "127.0.0.1:16379"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client := redis.NewClient(&redis.Options{Addr: address})
	t.Cleanup(func() { _ = client.Close() })
	if err := client.Ping(ctx).Err(); err != nil {
		t.Fatalf("Redis integration unavailable at %s: %v", address, err)
	}

	service := &Service{
		fingerprintKey: deriveFingerprintKey("invalid-key-integration-secret-with-at-least-thirty-two-bytes"),
		now:            time.Now,
		rdb:            client,
	}
	apiCode := "integration_invalid_key_guard"
	clientIP := "203.0.113.254"
	keyHash := secretHash("bp_live_integration_invalid_key")
	attemptKey := service.invalidKeyAttemptKey(apiCode, clientIP)
	cacheKey := invalidKeyCacheKey(keyHash)
	attemptMetricKey := invalidKeyMetricKey("attempts", service.currentTime())
	blockedMetricKey := invalidKeyMetricKey("blocked", service.currentTime())
	t.Cleanup(func() {
		_ = client.Del(context.Background(), attemptKey, cacheKey, attemptMetricKey, blockedMetricKey).Err()
	})
	if err := client.Del(ctx, attemptKey, cacheKey, attemptMetricKey, blockedMetricKey).Err(); err != nil {
		t.Fatal(err)
	}

	if err := service.guardInvalidKeyLookup(ctx, apiCode, clientIP, keyHash); err != nil {
		t.Fatalf("fresh guard rejected lookup: %v", err)
	}
	if err := service.rememberInvalidKey(ctx, apiCode, clientIP, keyHash); err != nil {
		t.Fatalf("rememberInvalidKey() error = %v", err)
	}
	assertBusinessStatus(t, service.guardInvalidKeyLookup(ctx, apiCode, clientIP, keyHash), consts.StatusForbidden)
	if err := client.Set(ctx, attemptKey, invalidKeyAttemptLimit, invalidKeyAttemptTTL).Err(); err != nil {
		t.Fatal(err)
	}
	assertBusinessStatus(t, service.guardInvalidKeyLookup(ctx, apiCode, clientIP, keyHash), consts.StatusTooManyRequests)
	if attempts := client.Get(ctx, attemptMetricKey).Val(); attempts != "3" {
		t.Fatalf("invalid-key attempt metric = %q, want 3", attempts)
	}
	if blocked := client.Get(ctx, blockedMetricKey).Val(); blocked != "1" {
		t.Fatalf("invalid-key blocked metric = %q, want 1", blocked)
	}
	if ttl := client.TTL(ctx, attemptKey).Val(); ttl <= 0 || ttl > invalidKeyAttemptTTL {
		t.Fatalf("invalid attempt TTL = %v", ttl)
	}
}

func assertBusinessStatus(t *testing.T, err error, status int) {
	t.Helper()
	var business *apperrors.BusinessError
	if !errors.As(err, &business) || business.StatusCode != status {
		t.Fatalf("error = %v, want business status %d", err, status)
	}
}
