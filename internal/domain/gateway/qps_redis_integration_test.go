package gateway

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	apperrors "bugpk-api/internal/pkg/errors"

	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"github.com/redis/go-redis/v9"
)

func TestQPSReleaseUsesIndependentContextRedisIntegration(t *testing.T) {
	if os.Getenv("BUGPK_GATEWAY_REDIS_INTEGRATION") != "1" {
		t.Skip("set BUGPK_GATEWAY_REDIS_INTEGRATION=1 to run Redis integration")
	}
	address := os.Getenv("REDIS_ADDR")
	if address == "" {
		address = "127.0.0.1:16379"
	}
	client := redis.NewClient(&redis.Options{Addr: address})
	t.Cleanup(func() { _ = client.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		t.Fatalf("Redis integration unavailable at %s: %v", address, err)
	}

	key := "openapi:quota:qps:integration:cancelled-context"
	t.Cleanup(func() { _ = client.Del(context.Background(), key).Err() })
	if err := client.Set(ctx, key, 1, time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	cancelledCtx, cancelRequest := context.WithCancel(context.Background())
	cancelRequest()
	service := &Service{rdb: client}
	service.releaseReservedQPS(cancelledCtx, qpsReservation{Keys: []string{key}, Counted: true, Reserved: true})
	if _, err := client.Get(context.Background(), key).Result(); err != redis.Nil {
		t.Fatalf("QPS reservation survived cancelled request context: %v", err)
	}
}

func TestQPSRejectsAboveLimitAndKeepsCounterAtLimitRedisIntegration(t *testing.T) {
	if os.Getenv("BUGPK_GATEWAY_REDIS_INTEGRATION") != "1" {
		t.Skip("set BUGPK_GATEWAY_REDIS_INTEGRATION=1 to run Redis integration")
	}
	address := os.Getenv("REDIS_ADDR")
	if address == "" {
		address = "127.0.0.1:16379"
	}
	client := redis.NewClient(&redis.Options{Addr: address})
	t.Cleanup(func() { _ = client.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		t.Fatalf("Redis integration unavailable at %s: %v", address, err)
	}

	fixedNow := time.Unix(1_800_000_000, 0)
	service := &Service{rdb: client, now: func() time.Time { return fixedNow }}
	quota := BillingReservation{ChargeType: "GLOBAL_PACKAGE", PackageRecordID: 94, QPSLimit: 2}
	key := service.accountQPSKey(91)
	t.Cleanup(func() { _ = client.Del(context.Background(), key).Err() })
	if err := client.Del(ctx, key).Err(); err != nil {
		t.Fatal(err)
	}

	first, err := service.reserveQPS(ctx, 91, quota)
	if err != nil {
		t.Fatalf("first QPS reservation failed: %v", err)
	}
	if _, err := service.reserveQPS(ctx, 91, quota); err != nil {
		t.Fatalf("second QPS reservation failed: %v", err)
	}
	_, err = service.reserveQPS(ctx, 91, quota)
	var business *apperrors.BusinessError
	if !errors.As(err, &business) || business.StatusCode != consts.StatusTooManyRequests {
		t.Fatalf("third QPS reservation error = %v, want HTTP 429", err)
	}
	if count, err := client.Get(ctx, key).Int64(); err != nil || count != 2 {
		t.Fatalf("counter after rejection = %d, %v; want 2", count, err)
	}

	service.releaseReservedQPS(ctx, first)
	if _, err := service.reserveQPS(ctx, 91, quota); err != nil {
		t.Fatalf("reservation after release failed: %v", err)
	}
}

func TestDefaultUserQPSCannotBeBypassedByAnotherKeyOrInterfaceRedisIntegration(t *testing.T) {
	if os.Getenv("BUGPK_GATEWAY_REDIS_INTEGRATION") != "1" {
		t.Skip("set BUGPK_GATEWAY_REDIS_INTEGRATION=1 to run Redis integration")
	}
	address := os.Getenv("REDIS_ADDR")
	if address == "" {
		address = "127.0.0.1:16379"
	}
	client := redis.NewClient(&redis.Options{Addr: address})
	t.Cleanup(func() { _ = client.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		t.Fatalf("Redis integration unavailable at %s: %v", address, err)
	}

	tests := []struct {
		name   string
		userID int64
	}{
		{name: "another API key", userID: 191},
		{name: "another interface", userID: 192},
	}

	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixedNow := time.Unix(1_810_000_000+int64(index), 0)
			service := &Service{rdb: client, now: func() time.Time { return fixedNow }}
			quota := BillingReservation{QPSLimit: 1}
			first, err := service.reserveQPS(ctx, test.userID, quota)
			if err != nil {
				t.Fatalf("first default-QPS reservation failed: %v", err)
			}
			t.Cleanup(func() {
				pattern := fmt.Sprintf("openapi:quota:qps:*:%d", fixedNow.Unix())
				keys, _, scanErr := client.Scan(context.Background(), 0, pattern, 100).Result()
				if scanErr == nil && len(keys) > 0 {
					_ = client.Del(context.Background(), keys...).Err()
				}
			})
			defer service.releaseReservedQPS(ctx, first)

			_, err = service.reserveQPS(ctx, test.userID, quota)
			var business *apperrors.BusinessError
			if !errors.As(err, &business) || business.StatusCode != consts.StatusTooManyRequests {
				t.Fatalf("second default-QPS reservation error = %v, want HTTP 429", err)
			}
		})
	}
}

func TestCustomInterfaceQPSIsAtomicAndIsolatedPerUserRedisIntegration(t *testing.T) {
	if os.Getenv("BUGPK_GATEWAY_REDIS_INTEGRATION") != "1" {
		t.Skip("set BUGPK_GATEWAY_REDIS_INTEGRATION=1 to run Redis integration")
	}
	address := os.Getenv("REDIS_ADDR")
	if address == "" {
		address = "127.0.0.1:16379"
	}
	client := redis.NewClient(&redis.Options{Addr: address})
	t.Cleanup(func() { _ = client.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		t.Fatalf("Redis integration unavailable at %s: %v", address, err)
	}

	fixedNow := time.Unix(1_820_000_000, 0)
	service := &Service{rdb: client, now: func() time.Time { return fixedNow }}
	userID := int64(701)
	api := InterfaceConfig{ID: 601, UserQPSLimit: 17}
	key := service.interfaceUserQPSKey(userID, api.ID)
	otherInterfaceKey := service.interfaceUserQPSKey(userID, 602)
	otherUserKey := service.interfaceUserQPSKey(702, api.ID)
	t.Cleanup(func() { _ = client.Del(context.Background(), key, otherInterfaceKey, otherUserKey).Err() })
	if err := client.Del(ctx, key, otherInterfaceKey, otherUserKey).Err(); err != nil {
		t.Fatal(err)
	}

	var allowed atomic.Int64
	var rejected atomic.Int64
	var unexpected atomic.Int64
	var wait sync.WaitGroup
	for range 64 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := service.reserveRequestQPS(ctx, userID, api, BillingReservation{QPSLimit: 0, Reserved: true})
			if err == nil {
				allowed.Add(1)
				return
			}
			var business *apperrors.BusinessError
			if errors.As(err, &business) && business.StatusCode == consts.StatusTooManyRequests {
				rejected.Add(1)
				return
			}
			unexpected.Add(1)
		}()
	}
	wait.Wait()
	if got := allowed.Load(); got != int64(api.UserQPSLimit) {
		t.Fatalf("allowed calls = %d, want %d", got, api.UserQPSLimit)
	}
	if got := rejected.Load(); got != 64-int64(api.UserQPSLimit) {
		t.Fatalf("rejected calls = %d, want %d", got, 64-int64(api.UserQPSLimit))
	}
	if got := unexpected.Load(); got != 0 {
		t.Fatalf("unexpected errors = %d", got)
	}
	if count, err := client.Get(ctx, key).Int64(); err != nil || count != int64(api.UserQPSLimit) {
		t.Fatalf("interface counter = %d, %v; want %d", count, err, api.UserQPSLimit)
	}
	if ttl, err := client.TTL(ctx, key).Result(); err != nil || ttl <= 0 {
		t.Fatalf("interface counter TTL = %v, %v; want positive", ttl, err)
	}
	if _, err := service.reserveRequestQPS(ctx, userID, InterfaceConfig{ID: 602, UserQPSLimit: 1}, BillingReservation{QPSLimit: 0, Reserved: true}); err != nil {
		t.Fatalf("different interface unexpectedly shared the counter: %v", err)
	}
	if _, err := service.reserveRequestQPS(ctx, 702, api, BillingReservation{QPSLimit: 0, Reserved: true}); err != nil {
		t.Fatalf("different user unexpectedly shared the counter: %v", err)
	}
}
