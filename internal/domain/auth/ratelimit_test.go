package auth

import (
	"context"
	"strings"
	"testing"
	"time"

	apperrors "bugpk-api/internal/pkg/errors"

	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"github.com/redis/go-redis/v9"
)

func TestRateLimitIdentitiesArePrivateFixedLengthAndCanonical(t *testing.T) {
	limiter := NewRateLimiter(nil, "first-test-secret-with-at-least-thirty-two-bytes")
	ip := limiter.rateLimitIPIdentity("2001:0db8:0:0:0:0:0:1")
	if ip != limiter.rateLimitIPIdentity("2001:db8::1") {
		t.Fatal("equivalent IP addresses produced different rate-limit identities")
	}
	account := limiter.rateLimitAccountIdentity(" Admin@Example.COM ")
	if account != limiter.rateLimitAccountIdentity("admin@example.com") {
		t.Fatal("equivalent accounts produced different rate-limit identities")
	}
	for name, value := range map[string]string{"ip": ip, "account": account} {
		if len(value) != 64 {
			t.Fatalf("%s identity length = %d, want 64", name, len(value))
		}
		for _, sensitive := range []string{"2001:db8", "admin", "example.com"} {
			if strings.Contains(strings.ToLower(value), sensitive) {
				t.Fatalf("%s identity leaked %q: %q", name, sensitive, value)
			}
		}
	}
	if ip == limiter.rateLimitAccountIdentity("2001:db8::1") {
		t.Fatal("IP and account rate-limit identity domains collided")
	}
	other := NewRateLimiter(nil, "second-test-secret-with-at-least-thirty-two-bytes")
	if ip == other.rateLimitIPIdentity("2001:db8::1") {
		t.Fatal("different server secrets produced the same rate-limit identity")
	}
}

func TestLoginRateLimitWritesFailClosedWhenRedisIsUnavailable(t *testing.T) {
	client := redis.NewClient(&redis.Options{
		Addr:         "127.0.0.1:1",
		DialTimeout:  20 * time.Millisecond,
		MaxRetries:   -1,
		ReadTimeout:  20 * time.Millisecond,
		WriteTimeout: 20 * time.Millisecond,
	})
	t.Cleanup(func() { _ = client.Close() })
	limiter := NewRateLimiter(client, "rate-limit-test-secret-with-at-least-thirty-two-bytes")

	for name, operation := range map[string]func() error{
		"failure record": func() error {
			return limiter.RecordLoginFailure(context.Background(), "127.0.0.1", "admin")
		},
		"success cleanup": func() error {
			return limiter.RecordLoginSuccess(context.Background(), "admin")
		},
		"password reset account check": func() error {
			return limiter.CheckPasswordResetAccount(context.Background(), "127.0.0.1")
		},
		"refresh check": func() error {
			return limiter.CheckRefresh(context.Background(), "127.0.0.1")
		},
	} {
		t.Run(name, func(t *testing.T) {
			err := operation()
			business, ok := apperrors.AsBusinessError(err)
			if !ok || business.StatusCode != consts.StatusServiceUnavailable {
				t.Fatalf("operation error = %v, want service unavailable", err)
			}
		})
	}
}
