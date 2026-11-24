package redisutil

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

var ErrClientUnavailable = errors.New("redis client unavailable")

var incrWithTTLScript = redis.NewScript(`
local current = redis.call("INCR", KEYS[1])
if current == 1 or redis.call("PTTL", KEYS[1]) < 0 then
  redis.call("PEXPIRE", KEYS[1], ARGV[1])
end
return current
`)

var decrFloorScript = redis.NewScript(`
if redis.call("EXISTS", KEYS[1]) == 0 then
  return 0
end
local current = redis.call("DECR", KEYS[1])
if current <= 0 then
  redis.call("DEL", KEYS[1])
  return 0
end
return current
`)

func IncrWithTTL(ctx context.Context, rdb *redis.Client, key string, ttl time.Duration) (int64, error) {
	key = strings.TrimSpace(key)
	if rdb == nil || key == "" {
		return 0, ErrClientUnavailable
	}
	ms := ttlMilliseconds(ttl)
	if ms <= 0 {
		return 0, errors.New("redis counter ttl must be positive")
	}
	return incrWithTTLScript.Run(ctx, rdb, []string{key}, ms).Int64()
}

func DecrFloor(ctx context.Context, rdb *redis.Client, key string) (int64, error) {
	key = strings.TrimSpace(key)
	if rdb == nil || key == "" {
		return 0, ErrClientUnavailable
	}
	return decrFloorScript.Run(ctx, rdb, []string{key}).Int64()
}

func ttlMilliseconds(ttl time.Duration) int64 {
	if ttl <= 0 {
		return 0
	}
	ms := ttl.Milliseconds()
	if ms <= 0 {
		return 1
	}
	return ms
}
