package auth

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	oauthStateKeyPrefix   = "auth:oauth:state:"
	oauthLoginKeyPrefix   = "auth:oauth:login:"
	oauthPendingKeyPrefix = "auth:oauth:pending:"
)

var oauthConsumeScript = redis.NewScript(`
local value = redis.call("GET", KEYS[1])
if value then
  redis.call("DEL", KEYS[1])
end
return value
`)

type oauthStateStore struct {
	rdb *redis.Client
}

func newOAuthStateStore(rdb *redis.Client) *oauthStateStore {
	return &oauthStateStore{rdb: rdb}
}

func (s *oauthStateStore) save(ctx context.Context, key string, value any, ttl time.Duration) error {
	if s == nil || s.rdb == nil {
		return errors.New("oauth state service is unavailable")
	}
	if ttl <= 0 {
		return errors.New("oauth state ttl must be positive")
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return s.rdb.Set(ctx, key, encoded, ttl).Err()
}

func (s *oauthStateStore) consume(ctx context.Context, key string, target any) (bool, error) {
	if s == nil || s.rdb == nil {
		return false, errors.New("oauth state service is unavailable")
	}
	raw, err := oauthConsumeScript.Run(ctx, s.rdb, []string{key}).Text()
	if errors.Is(err, redis.Nil) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if raw == "" {
		return false, nil
	}
	if err := json.Unmarshal([]byte(raw), target); err != nil {
		return false, err
	}
	return true, nil
}

func (s *oauthStateStore) peek(ctx context.Context, key string, target any) (bool, error) {
	if s == nil || s.rdb == nil {
		return false, errors.New("oauth state service is unavailable")
	}
	raw, err := s.rdb.Get(ctx, key).Bytes()
	if errors.Is(err, redis.Nil) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return false, err
	}
	return true, nil
}

func (s *oauthStateStore) restore(ctx context.Context, key string, value any, ttl time.Duration) error {
	if ttl <= 0 {
		ttl = time.Second
	}
	return s.save(ctx, key, value, ttl)
}

func oauthStateKey(state string) string    { return oauthStateKeyPrefix + state }
func oauthLoginKey(ticket string) string   { return oauthLoginKeyPrefix + ticket }
func oauthPendingKey(ticket string) string { return oauthPendingKeyPrefix + ticket }
