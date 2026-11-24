package middleware

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"

	"bugpk-api/internal/pkg/response"

	hertzapp "github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"github.com/redis/go-redis/v9"
)

const publicRateLimitScript = `
local current = redis.call('INCR', KEYS[1])
if current == 1 then
  redis.call('PEXPIRE', KEYS[1], ARGV[1])
end
local ttl = redis.call('PTTL', KEYS[1])
return {current, ttl}
`

// PublicContentRateLimit protects anonymous, database-backed read endpoints.
// The key contains only an HMAC digest of the client IP so Redis never stores
// the original address as part of the limiter state.
func PublicContentRateLimit(client *redis.Client, systemPrefix, secret string, limit int64, window time.Duration) hertzapp.HandlerFunc {
	prefix := strings.TrimRight(strings.TrimSpace(systemPrefix), "/")
	if prefix == "" {
		prefix = "/system-api"
	}
	if limit <= 0 {
		limit = 120
	}
	if window <= 0 {
		window = time.Minute
	}

	return func(ctx context.Context, c *hertzapp.RequestContext) {
		path := string(c.Path())
		bucket, ok := publicContentBucket(path, prefix)
		if !ok {
			c.Next(ctx)
			return
		}
		if client == nil {
			response.FailServerError(c, consts.StatusServiceUnavailable, fmt.Errorf("public rate limiter unavailable"))
			c.Abort()
			return
		}

		key := "public-rate:" + bucket + ":" + publicClientDigest(secret, c.ClientIP())
		values, err := client.Eval(ctx, publicRateLimitScript, []string{key}, window.Milliseconds()).Slice()
		if err != nil || len(values) != 2 {
			response.FailServerError(c, consts.StatusServiceUnavailable, err)
			c.Abort()
			return
		}

		current, currentOK := redisInt64(values[0])
		ttlMS, ttlOK := redisInt64(values[1])
		if !currentOK || !ttlOK {
			response.FailServerError(c, consts.StatusServiceUnavailable, fmt.Errorf("public rate limiter returned unexpected shape"))
			c.Abort()
			return
		}
		if ttlMS < 0 {
			ttlMS = window.Milliseconds()
		}
		remaining := limit - current
		if remaining < 0 {
			remaining = 0
		}
		c.Response.Header.Set("RateLimit-Limit", strconv.FormatInt(limit, 10))
		c.Response.Header.Set("RateLimit-Remaining", strconv.FormatInt(remaining, 10))
		c.Response.Header.Set("RateLimit-Reset", strconv.FormatInt((ttlMS+999)/1000, 10))

		if current > limit {
			c.Response.Header.Set("Retry-After", strconv.FormatInt((ttlMS+999)/1000, 10))
			response.Fail(c, consts.StatusTooManyRequests, "请求过于频繁，请稍后再试")
			c.Abort()
			return
		}
		c.Next(ctx)
	}
}

func publicContentBucket(path, prefix string) (string, bool) {
	switch {
	case path == prefix+"/site/config":
		return "site", true
	case path == prefix+"/home/overview":
		return "home", true
	case path == prefix+"/donation/config":
		return "donation", true
	case path == prefix+"/payment/currency":
		return "payment-currency", true
	case path == prefix+"/donation/supporters":
		return "donation", true
	case path == prefix+"/notice/user/list" || path == prefix+"/notice/user/popup":
		return "notice", true
	case strings.HasPrefix(path, prefix+"/notice/user/"):
		return "notice", true
	case path == prefix+"/friend-links/public/list" || path == prefix+"/friend-links/public/meta":
		return "friend-links", true
	default:
		return "", false
	}
}

func publicClientDigest(secret, ip string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(strings.TrimSpace(ip)))
	return hex.EncodeToString(mac.Sum(nil)[:16])
}

func redisInt64(value any) (int64, bool) {
	switch typed := value.(type) {
	case int64:
		return typed, true
	case string:
		parsed, err := strconv.ParseInt(typed, 10, 64)
		return parsed, err == nil
	case []byte:
		parsed, err := strconv.ParseInt(string(typed), 10, 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}
