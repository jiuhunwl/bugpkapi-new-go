package riskcontrol

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func TestInvalidKeyMetricsRedisIntegration(t *testing.T) {
	if os.Getenv("BUGPK_RISK_REDIS_INTEGRATION") != "1" {
		t.Skip("set BUGPK_RISK_REDIS_INTEGRATION=1 to run Redis integration")
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

	now := time.Unix(1_900_000_000, 0)
	keys := []string{
		invalidKeyMetricKeyForTest("attempts", now),
		invalidKeyMetricKeyForTest("blocked", now),
		invalidKeyMetricKeyForTest("attempts", now.Add(-time.Minute)),
		invalidKeyMetricKeyForTest("blocked", now.Add(-time.Minute)),
		invalidKeyMetricKeyForTest("attempts", now.Add(-10*time.Minute)),
		invalidKeyMetricKeyForTest("attempts", now.Add(-11*time.Minute)),
	}
	t.Cleanup(func() { _ = client.Del(context.Background(), keys...).Err() })
	if err := client.Del(ctx, keys...).Err(); err != nil {
		t.Fatal(err)
	}
	values := map[string]any{
		keys[0]: int64(7),
		keys[1]: int64(2),
		keys[2]: int64(5),
		keys[3]: int64(1),
		keys[4]: int64(88),
		keys[5]: int64(99),
	}
	for key, value := range values {
		if err := client.Set(ctx, key, value, time.Minute).Err(); err != nil {
			t.Fatal(err)
		}
	}
	service := &Service{rdb: client, now: func() time.Time { return now }}
	attempts, blocked, err := service.invalidKeyMetrics(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if attempts != 12 || blocked != 3 {
		t.Fatalf("invalid-key metrics = %d/%d, want 12/3", attempts, blocked)
	}
	started := time.Now()
	attempts, blocked, err = service.invalidKeyMetrics(ctx, 1440)
	if err != nil {
		t.Fatal(err)
	}
	if attempts != 199 || blocked != 3 {
		t.Fatalf("24-hour invalid-key metrics = %d/%d, want 199/3", attempts, blocked)
	}
	t.Logf("invalid_key_24h_elapsed=%s", time.Since(started).Round(time.Microsecond))
}

func invalidKeyMetricKeyForTest(metric string, now time.Time) string {
	return "gateway:risk:invalid-key:metric:" + metric + ":" + strconv.FormatInt(now.Unix()/60, 10)
}
