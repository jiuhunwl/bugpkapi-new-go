package upstreamhealth

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	StatusHealthy   = "HEALTHY"
	StatusUnhealthy = "UNHEALTHY"
	StatusUnknown   = "UNKNOWN"

	failureThreshold        = 2
	failureCooldownDuration = time.Hour
	metricTTL               = 10 * time.Minute
)

const recordMetricLua = `
local expireMillis = tonumber(ARGV[4])
local failureThreshold = tonumber(ARGV[6])
local failureCooldown = tonumber(ARGV[7])
if ARGV[1] == '1' then
  redis.call('HSET', KEYS[1],
    'status', 'HEALTHY',
    'latencyMs', ARGV[2],
    'checkedAt', ARGV[3],
    'failureCount', '0',
    'cooldownUntil', '')
else
  redis.call('HSET', KEYS[1],
    'status', 'UNHEALTHY',
    'latencyMs', ARGV[2],
    'checkedAt', ARGV[3])
  local failures = redis.call('HINCRBY', KEYS[1], 'failureCount', 1)
  local cooldown = 0
  if failures >= failureThreshold then
    cooldown = failureCooldown
  end
  local cooldownUntil = ''
  if cooldown > 0 then
    cooldownUntil = tostring(tonumber(ARGV[5]) + cooldown)
  end
  redis.call('HSET', KEYS[1], 'cooldownUntil', cooldownUntil)
  if cooldown > 0 then
    expireMillis = cooldown * 1000
  end
end
redis.call('PEXPIRE', KEYS[1], expireMillis)
return 1
`

var recordMetricScript = redis.NewScript(recordMetricLua)

type Metric struct {
	CheckedAt     string
	CooldownUntil string
	FailureCount  int
	LatencyMs     int64
	Status        string
}

type ReadResult struct {
	Err    error
	Found  bool
	Metric Metric
}

func Compare(left Metric, leftFound bool, right Metric, rightFound bool) int {
	return CompareAt(left, leftFound, right, rightFound, time.Now())
}

func CompareAt(left Metric, leftFound bool, right Metric, rightFound bool, now time.Time) int {
	leftRank := Rank(left, leftFound, now)
	rightRank := Rank(right, rightFound, now)
	if leftRank < rightRank {
		return -1
	}
	if leftRank > rightRank {
		return 1
	}
	if leftRank == 1 {
		leftUnknown := !leftFound || strings.EqualFold(strings.TrimSpace(left.Status), StatusUnknown)
		rightUnknown := !rightFound || strings.EqualFold(strings.TrimSpace(right.Status), StatusUnknown)
		if leftUnknown != rightUnknown {
			if leftUnknown {
				return -1
			}
			return 1
		}
	}
	if leftFound && left.LatencyMs != right.LatencyMs {
		if left.LatencyMs < right.LatencyMs {
			return -1
		}
		return 1
	}
	if left.FailureCount < right.FailureCount {
		return -1
	}
	if left.FailureCount > right.FailureCount {
		return 1
	}
	return 0
}

func Rank(metric Metric, found bool, now time.Time) int {
	if !found {
		return 1
	}
	if activeCooldown(metric.CooldownUntil, now) {
		return 2
	}
	switch strings.ToUpper(metric.Status) {
	case StatusHealthy:
		return 0
	default:
		return 1
	}
}

func failureCooldown(failures int) time.Duration {
	if failures < failureThreshold {
		return 0
	}
	return failureCooldownDuration
}

func Read(ctx context.Context, rdb *redis.Client, apiID int64, upstreamURL string) (Metric, bool, error) {
	return ReadScoped(ctx, rdb, apiID, "", upstreamURL)
}

// ReadScoped reads health metadata for one upstream inside an optional pool
// scope. The empty scope preserves the legacy default-pool key; a non-empty
// scope isolates domain-routed sub-pools even when they reuse an upstream URL.
func ReadScoped(ctx context.Context, rdb *redis.Client, apiID int64, scope, upstreamURL string) (Metric, bool, error) {
	if rdb == nil || apiID <= 0 || strings.TrimSpace(upstreamURL) == "" {
		return Metric{}, false, nil
	}
	values, err := rdb.HGetAll(ctx, scopedKey(apiID, scope, upstreamURL)).Result()
	if err != nil {
		return Metric{}, false, err
	}
	if len(values) == 0 {
		return Metric{}, false, nil
	}
	metric, err := metricFromValues(values)
	if err != nil {
		return Metric{}, false, err
	}
	return metric, true, nil
}

func ReadMany(ctx context.Context, rdb *redis.Client, apiID int64, upstreamURLs []string) ([]ReadResult, error) {
	return ReadManyScoped(ctx, rdb, apiID, "", upstreamURLs)
}

// ReadManyScoped is the batch form of ReadScoped. Results retain the input
// order so callers can safely correlate health metadata with candidate nodes.
func ReadManyScoped(ctx context.Context, rdb *redis.Client, apiID int64, scope string, upstreamURLs []string) ([]ReadResult, error) {
	results := make([]ReadResult, len(upstreamURLs))
	if rdb == nil || apiID <= 0 || len(upstreamURLs) == 0 {
		return results, nil
	}
	pipe := rdb.Pipeline()
	commands := make([]*redis.MapStringStringCmd, len(upstreamURLs))
	hasCommand := false
	for index, upstreamURL := range upstreamURLs {
		if strings.TrimSpace(upstreamURL) == "" {
			continue
		}
		commands[index] = pipe.HGetAll(ctx, scopedKey(apiID, scope, upstreamURL))
		hasCommand = true
	}
	if !hasCommand {
		return results, nil
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return nil, err
	}
	for index, command := range commands {
		if command == nil {
			continue
		}
		values, err := command.Result()
		if err != nil {
			results[index].Err = err
			continue
		}
		if len(values) == 0 {
			continue
		}
		metric, err := metricFromValues(values)
		if err != nil {
			results[index].Err = err
			continue
		}
		results[index] = ReadResult{Found: true, Metric: metric}
	}
	return results, nil
}

func Record(ctx context.Context, rdb *redis.Client, apiID int64, upstreamURL string, healthy bool, latencyMs int64) error {
	return RecordScoped(ctx, rdb, apiID, "", upstreamURL, healthy, latencyMs)
}

// RecordScoped persists a health sample in an optional pool scope. Route IDs
// are intentionally part of the Redis key material rather than the upstream
// URL, because two independent domain routes may point at the same endpoint
// while requiring separate failure counters and cooldowns.
func RecordScoped(ctx context.Context, rdb *redis.Client, apiID int64, scope, upstreamURL string, healthy bool, latencyMs int64) error {
	if rdb == nil || apiID <= 0 || strings.TrimSpace(upstreamURL) == "" {
		return nil
	}
	now := time.Now()
	redisKey := scopedKey(apiID, scope, upstreamURL)
	healthyValue := "0"
	if healthy {
		healthyValue = "1"
	}
	return recordMetricScript.Run(ctx, rdb, []string{redisKey},
		healthyValue,
		strconv.FormatInt(nonNegativeLatency(latencyMs), 10),
		now.Format(time.RFC3339),
		metricTTL.Milliseconds(),
		now.Unix(),
		failureThreshold,
		int64(failureCooldownDuration/time.Second),
	).Err()
}

func metricFromValues(values map[string]string) (Metric, error) {
	status := strings.ToUpper(strings.TrimSpace(values["status"]))
	if status == "" {
		status = StatusUnknown
	}
	if status != StatusHealthy && status != StatusUnhealthy && status != StatusUnknown {
		return Metric{}, fmt.Errorf("invalid upstream health status %q", status)
	}
	latency, err := nonNegativeInt64(values["latencyMs"], "latencyMs")
	if err != nil {
		return Metric{}, err
	}
	failures64, err := nonNegativeInt64(values["failureCount"], "failureCount")
	if err != nil {
		return Metric{}, err
	}
	if failures64 > int64(^uint(0)>>1) {
		return Metric{}, fmt.Errorf("upstream health failureCount is too large: %d", failures64)
	}
	checkedAt := strings.TrimSpace(values["checkedAt"])
	if checkedAt != "" {
		if _, err := time.Parse(time.RFC3339, checkedAt); err != nil {
			return Metric{}, fmt.Errorf("invalid upstream health checkedAt: %w", err)
		}
	}
	cooldownUntil := strings.TrimSpace(values["cooldownUntil"])
	if cooldownUntil != "" {
		if _, err := parseCooldownUntil(cooldownUntil); err != nil {
			return Metric{}, fmt.Errorf("invalid upstream health cooldownUntil: %w", err)
		}
	}
	return Metric{
		CheckedAt:     checkedAt,
		CooldownUntil: cooldownUntil,
		FailureCount:  int(failures64),
		LatencyMs:     latency,
		Status:        status,
	}, nil
}

func activeCooldown(value string, now time.Time) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	deadline, err := parseCooldownUntil(value)
	if err != nil {
		return true
	}
	return deadline.After(now)
}

// InCooldown 报告指定节点当前是否处于失败冷却期。cooldownUntil 为空或已过期时返回 false。
func InCooldown(metric Metric, now time.Time) bool {
	return activeCooldown(metric.CooldownUntil, now)
}

func parseCooldownUntil(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if unix, err := strconv.ParseInt(value, 10, 64); err == nil && unix >= 0 {
		return time.Unix(unix, 0), nil
	}
	return time.Parse(time.RFC3339, value)
}

func nonNegativeInt64(value, field string) (int64, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed < 0 {
		return 0, fmt.Errorf("invalid upstream health %s %q", field, value)
	}
	return parsed, nil
}

func key(apiID int64, upstreamURL string) string {
	return scopedKey(apiID, "", upstreamURL)
}

func scopedKey(apiID int64, scope, upstreamURL string) string {
	material := strings.TrimSpace(upstreamURL)
	if scope = strings.TrimSpace(scope); scope != "" {
		material = scope + "\x00" + material
	}
	sum := sha1.Sum([]byte(material))
	return fmt.Sprintf("upstream:health:%d:%s", apiID, hex.EncodeToString(sum[:]))
}

func nonNegativeLatency(value int64) int64 {
	if value < 0 {
		return 0
	}
	return value
}
