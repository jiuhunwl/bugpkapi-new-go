package requestrisk

import (
	"context"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func TestBucketAndKeyDoNotContainRawRequestData(t *testing.T) {
	now := time.Date(2026, time.August, 22, 12, 34, 56, 0, time.UTC)
	fingerprint := strings.Repeat("a", 64)
	bucket := bucketFor(now)
	wantBucket := strconv.FormatInt(now.Unix()/riskWindowSeconds, 10)
	if bucket != wantBucket {
		t.Fatalf("bucketFor() = %q, want %q", bucket, wantBucket)
	}
	key := counterKey(17, 29, fingerprint, bucket)
	if !strings.Contains(key, "17") || !strings.Contains(key, "29") || !strings.Contains(key, fingerprint) || !strings.Contains(key, bucket) {
		t.Fatalf("counter key does not preserve safe dimensions: %q", key)
	}
	for _, raw := range []string{"phone", "13800138000", "203.0.113.8", "password"} {
		if strings.Contains(key, raw) {
			t.Fatalf("counter key leaked raw request data %q: %q", raw, key)
		}
	}
}

func TestCountScriptUsesAtomicThresholdAndWindowTTL(t *testing.T) {
	for _, fragment := range []string{
		`redis.call("INCR", KEYS[1])`,
		`redis.call("PEXPIRE", KEYS[1], ARGV[1])`,
		`return current`,
	} {
		if !strings.Contains(countLua, fragment) {
			t.Fatalf("count Lua script lost %q", fragment)
		}
	}
	if thresholdCount != 8 || riskWindow != 5*time.Minute {
		t.Fatalf("risk policy changed: threshold=%d window=%v", thresholdCount, riskWindow)
	}
}

func TestCountRedisIntegration(t *testing.T) {
	addr := strings.TrimSpace(os.Getenv("REDIS_INTEGRATION_ADDR"))
	if addr == "" {
		t.Skip("REDIS_INTEGRATION_ADDR is not configured")
	}
	client := redis.NewClient(&redis.Options{Addr: addr, MaxRetries: -1})
	t.Cleanup(func() { _ = client.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		t.Fatalf("temporary Redis ping failed: %v", err)
	}
	now := time.Date(2026, time.August, 22, 12, 34, 56, 0, time.UTC)
	fingerprint := strings.Repeat("b", 64)
	key := counterKey(901, 902, fingerprint, bucketFor(now))
	t.Cleanup(func() { _ = client.Del(context.Background(), key).Err() })
	for request := int64(1); request <= thresholdCount+1; request++ {
		count, hit, bucket, err := Count(ctx, client, 901, 902, fingerprint, now)
		if err != nil {
			t.Fatalf("Count(%d) failed: %v", request, err)
		}
		if count != request || bucket != bucketFor(now) {
			t.Fatalf("Count(%d) = count:%d bucket:%q", request, count, bucket)
		}
		if hit != (request == thresholdCount) {
			t.Fatalf("Count(%d) thresholdHit=%v", request, hit)
		}
	}
	ttl, err := client.PTTL(ctx, key).Result()
	if err != nil || ttl <= 0 || ttl > riskWindow {
		t.Fatalf("counter TTL = %v, err=%v", ttl, err)
	}

	concurrentFingerprint := strings.Repeat("c", 64)
	concurrentKey := counterKey(903, 904, concurrentFingerprint, bucketFor(now))
	t.Cleanup(func() { _ = client.Del(context.Background(), concurrentKey).Err() })
	var hits atomic.Int64
	var failures atomic.Int64
	var wait sync.WaitGroup
	for range 64 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, hit, _, countErr := Count(context.Background(), client, 903, 904, concurrentFingerprint, now)
			if countErr != nil {
				failures.Add(1)
				return
			}
			if hit {
				hits.Add(1)
			}
		}()
	}
	wait.Wait()
	if failures.Load() != 0 || hits.Load() != 1 {
		t.Fatalf("concurrent count failures=%d threshold hits=%d, want 0/1", failures.Load(), hits.Load())
	}
}
