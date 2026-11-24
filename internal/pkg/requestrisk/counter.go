// Package requestrisk contains privacy-preserving, request-time risk counters.
package requestrisk

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	thresholdCount    int64 = 8
	riskWindow              = 5 * time.Minute
	riskWindowSeconds int64 = int64(riskWindow / time.Second)
)

var ErrInvalidDimensions = errors.New("request risk counter dimensions are invalid")

const countLua = `
local current = redis.call("INCR", KEYS[1])
if current == 1 or redis.call("PTTL", KEYS[1]) < 0 then
  redis.call("PEXPIRE", KEYS[1], ARGV[1])
end
return current
`

var countScript = redis.NewScript(countLua)

// Count atomically increments the five-minute counter for one interface, key,
// and request fingerprint. thresholdHit is true only for the request that
// changes the counter from 7 to 8; subsequent requests remain observable via
// count without generating duplicate threshold alerts.
func Count(ctx context.Context, rdb *redis.Client, interfaceID, keyID int64, fingerprint string, now time.Time) (count int64, thresholdHit bool, bucket string, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if now.IsZero() {
		now = time.Now()
	}
	bucket = bucketFor(now)
	if interfaceID <= 0 || keyID <= 0 || strings.TrimSpace(fingerprint) == "" {
		return 0, false, bucket, ErrInvalidDimensions
	}
	if rdb == nil {
		return 0, false, bucket, errors.New("request risk redis client unavailable")
	}
	key := counterKey(interfaceID, keyID, fingerprint, bucket)
	count, err = countScript.Run(ctx, rdb, []string{key}, riskWindow.Milliseconds()).Int64()
	if err != nil {
		return 0, false, bucket, err
	}
	return count, count == thresholdCount, bucket, nil
}

func bucketFor(now time.Time) string {
	return strconv.FormatInt(now.Unix()/riskWindowSeconds, 10)
}

func counterKey(interfaceID, keyID int64, fingerprint, bucket string) string {
	return fmt.Sprintf("gateway:risk:same-params:v1:%d:%d:%s:%s", interfaceID, keyID, normalizeFingerprint(fingerprint), strings.TrimSpace(bucket))
}

func normalizeFingerprint(value string) string {
	value = strings.TrimSpace(value)
	if decoded, err := hex.DecodeString(value); err == nil && len(decoded) == sha256.Size {
		return strings.ToLower(value)
	}
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
