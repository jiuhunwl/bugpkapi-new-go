package upstreamhealth

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func TestRecordMetricScriptKeepsMetricAndTTLAtomic(t *testing.T) {
	for _, fragment := range []string{
		"redis.call('HSET', KEYS[1]",
		"redis.call('HINCRBY', KEYS[1], 'failureCount', 1)",
		"redis.call('PEXPIRE', KEYS[1], expireMillis)",
		"'latencyMs', ARGV[2]",
		"'failureCount', '0'",
	} {
		if !strings.Contains(recordMetricLua, fragment) {
			t.Fatalf("record metric script lost %q", fragment)
		}
	}
	if strings.Contains(recordMetricLua, "EXPIRE") && !strings.Contains(recordMetricLua, "PEXPIRE") {
		t.Fatal("record metric script lost millisecond TTL precision")
	}
}

func TestRecordMetricRedisIntegration(t *testing.T) {
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

	const apiID = int64(99117)
	const upstreamURL = "https://integration.example.test/health"
	redisKey := key(apiID, upstreamURL)
	t.Cleanup(func() { _ = client.Del(context.Background(), redisKey).Err() })

	if err := Record(ctx, client, apiID, upstreamURL, false, 42); err != nil {
		t.Fatalf("first unhealthy record failed: %v", err)
	}
	if err := Record(ctx, client, apiID, upstreamURL, false, 44); err != nil {
		t.Fatalf("second unhealthy record failed: %v", err)
	}
	metric, ok, err := Read(ctx, client, apiID, upstreamURL)
	if err != nil || !ok {
		t.Fatalf("unhealthy metric read = %#v, %v, %v", metric, ok, err)
	}
	if metric.Status != StatusUnhealthy || metric.FailureCount != 2 || metric.LatencyMs != 44 {
		t.Fatalf("unexpected unhealthy metric: %#v", metric)
	}
	assertMetricOneHourCooldown(t, ctx, client, redisKey, metric)

	if err := Record(ctx, client, apiID, upstreamURL, true, -1); err != nil {
		t.Fatalf("healthy recovery record failed: %v", err)
	}
	metric, ok, err = Read(ctx, client, apiID, upstreamURL)
	if err != nil || !ok {
		t.Fatalf("healthy metric read = %#v, %v, %v", metric, ok, err)
	}
	if metric.Status != StatusHealthy || metric.FailureCount != 0 || metric.LatencyMs != 0 {
		t.Fatalf("unexpected healthy metric: %#v", metric)
	}
	assertMetricTTL(t, ctx, client, redisKey)
	if err := Record(ctx, client, apiID, upstreamURL, false, 0); err != nil {
		t.Fatalf("zero-latency unhealthy record failed: %v", err)
	}
	metric, ok, err = Read(ctx, client, apiID, upstreamURL)
	if err != nil || !ok {
		t.Fatalf("zero-latency unhealthy metric read = %#v, %v, %v", metric, ok, err)
	}
	if metric.Status != StatusUnhealthy || metric.FailureCount != 1 || metric.LatencyMs != 0 {
		t.Fatalf("unexpected zero-latency unhealthy metric: %#v", metric)
	}
	if err := Record(ctx, client, apiID, upstreamURL, true, 7); err != nil {
		t.Fatalf("second healthy recovery record failed: %v", err)
	}
	corruptURL := "https://integration.example.test/corrupt"
	corruptKey := key(apiID, corruptURL)
	t.Cleanup(func() { _ = client.Del(context.Background(), corruptKey).Err() })
	if err := client.HSet(ctx, corruptKey, "status", "BROKEN").Err(); err != nil {
		t.Fatalf("create corrupt metric: %v", err)
	}
	batch, err := ReadMany(ctx, client, apiID, []string{upstreamURL, corruptURL, "https://integration.example.test/missing", ""})
	if err != nil {
		t.Fatalf("batch metric read failed: %v", err)
	}
	if len(batch) != 4 || !batch[0].Found || batch[0].Err != nil || batch[0].Metric.Status != StatusHealthy {
		t.Fatalf("batch metric read = %#v", batch)
	}
	if batch[1].Found || batch[1].Err == nil {
		t.Fatalf("batch corrupt result = %#v", batch[1])
	}
	if batch[2].Found || batch[2].Err != nil || batch[3].Found || batch[3].Err != nil {
		t.Fatalf("batch missing/empty results = %#v", batch)
	}
}

func TestReadManyKeepsInputAlignmentWithoutRedis(t *testing.T) {
	results, err := ReadMany(context.Background(), nil, 1, []string{"https://one.example", "", "https://two.example"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 {
		t.Fatalf("result count = %d, want 3", len(results))
	}
	for index, result := range results {
		if result.Found || result.Err != nil || result.Metric != (Metric{}) {
			t.Fatalf("result[%d] = %#v, want empty", index, result)
		}
	}
}

func TestScopedHealthKeySeparatesDomainRoutePools(t *testing.T) {
	upstreamURL := "https://shared.example/api"
	defaultKey := key(99118, upstreamURL)
	routeAKey := scopedKey(99118, "route-a", upstreamURL)
	routeBKey := scopedKey(99118, "route-b", upstreamURL)
	if routeAKey == routeBKey || routeAKey == defaultKey || routeBKey == defaultKey {
		t.Fatalf("health keys are not isolated: default=%q routeA=%q routeB=%q", defaultKey, routeAKey, routeBKey)
	}
	if scopedKey(99118, "", upstreamURL) != defaultKey {
		t.Fatal("empty route scope should preserve the legacy default health key")
	}
}

func assertMetricTTL(t *testing.T, ctx context.Context, client *redis.Client, redisKey string) {
	t.Helper()
	ttl, err := client.PTTL(ctx, redisKey).Result()
	if err != nil {
		t.Fatalf("metric TTL read failed: %v", err)
	}
	if ttl <= 0 || ttl > metricTTL {
		t.Fatalf("metric TTL = %v, want within (0, %v]", ttl, metricTTL)
	}
}

func assertMetricOneHourCooldown(t *testing.T, ctx context.Context, client *redis.Client, redisKey string, metric Metric) {
	t.Helper()
	ttl, err := client.PTTL(ctx, redisKey).Result()
	if err != nil {
		t.Fatalf("cooldown metric TTL read failed: %v", err)
	}
	if ttl < 59*time.Minute || ttl > time.Hour {
		t.Fatalf("cooldown metric TTL = %v, want approximately one hour", ttl)
	}
	deadline, err := parseCooldownUntil(metric.CooldownUntil)
	if err != nil {
		t.Fatalf("cooldown deadline parse failed: %v", err)
	}
	remaining := time.Until(deadline)
	if remaining < 59*time.Minute || remaining > time.Hour {
		t.Fatalf("cooldown remaining = %v, want approximately one hour", remaining)
	}
}

func TestMetricFromValuesParsesValidMetric(t *testing.T) {
	checkedAt := time.Now().UTC().Truncate(time.Second).Format(time.RFC3339)
	metric, err := metricFromValues(map[string]string{
		"checkedAt":    checkedAt,
		"failureCount": "2",
		"latencyMs":    "31",
		"status":       "healthy",
	})
	if err != nil {
		t.Fatalf("metricFromValues() error = %v", err)
	}
	if metric.Status != StatusHealthy || metric.LatencyMs != 31 || metric.FailureCount != 2 || metric.CheckedAt != checkedAt {
		t.Fatalf("unexpected metric: %#v", metric)
	}
}

func TestMetricFromValuesRejectsCorruptValues(t *testing.T) {
	for name, values := range map[string]map[string]string{
		"status":   {"status": "BROKEN"},
		"latency":  {"latencyMs": "-1"},
		"failures": {"failureCount": "bad"},
		"checked":  {"checkedAt": "not-a-time"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := metricFromValues(values); err == nil {
				t.Fatal("metricFromValues accepted corrupt data")
			}
		})
	}
}

func TestCompareTreatsZeroLatencyAsFastestHealthyMetric(t *testing.T) {
	zero := Metric{Status: StatusHealthy, LatencyMs: 0}
	slower := Metric{Status: StatusHealthy, LatencyMs: 12}
	if Compare(zero, true, slower, true) >= 0 {
		t.Fatal("zero-millisecond healthy node was not preferred")
	}
	if Compare(slower, true, zero, true) <= 0 {
		t.Fatal("slower healthy node was preferred over zero-millisecond node")
	}
	if Compare(zero, true, Metric{}, false) >= 0 {
		t.Fatal("healthy node was not preferred over a node without metrics")
	}
	if Compare(Metric{}, false, Metric{Status: StatusUnhealthy}, true) >= 0 {
		t.Fatal("node without metrics should be attempted before a known unhealthy node")
	}
}

func TestFailureCooldownDurationIsOneHourAfterSecondFailure(t *testing.T) {
	want := map[int]time.Duration{
		0: 0,
		1: 0,
		2: time.Hour,
		3: time.Hour,
		9: time.Hour,
	}
	for failures, expected := range want {
		if got := failureCooldown(failures); got != expected {
			t.Fatalf("failureCooldown(%d) = %v, want %v", failures, got, expected)
		}
	}
}

func TestRankPlacesActiveCooldownAfterHealthyAndUnknownNodes(t *testing.T) {
	now := time.Date(2026, time.August, 20, 10, 0, 0, 0, time.UTC)
	if got := Rank(Metric{Status: StatusHealthy}, true, now); got != 0 {
		t.Fatalf("healthy rank = %d, want 0", got)
	}
	if got := Rank(Metric{}, false, now); got != 1 {
		t.Fatalf("unknown rank = %d, want 1", got)
	}
	if got := Rank(Metric{Status: StatusUnhealthy, FailureCount: 1}, true, now); got != 1 {
		t.Fatalf("first-failure rank = %d, want retryable rank 1", got)
	}
	cooling := Metric{
		Status:        StatusUnhealthy,
		FailureCount:  2,
		CooldownUntil: strconv.FormatInt(now.Add(15*time.Second).Unix(), 10),
	}
	if got := Rank(cooling, true, now); got != 2 {
		t.Fatalf("active cooldown rank = %d, want 2", got)
	}
	if got := Rank(cooling, true, now.Add(16*time.Second)); got != 1 {
		t.Fatalf("expired cooldown rank = %d, want retryable rank 1", got)
	}
}
