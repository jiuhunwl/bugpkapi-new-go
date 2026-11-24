package gateway

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func TestResponseCacheRoundTripRedisIntegration(t *testing.T) {
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

	service := &Service{rdb: client}
	api := InterfaceConfig{
		ID:                     900001,
		RequestURL:             "https://cache-integration.example/api",
		CacheEnabled:           1,
		CacheDefaultTTLSeconds: 60,
		CachePolicyVersion:     1,
	}
	req := OpenRequest{
		ClientIP: "203.0.113.90",
		Headers:  map[string]string{"Accept": "application/json", "X-Request-ID": "first"},
		Method:   "GET",
		Query:    map[string][]string{"q": {"round-trip"}},
	}
	key, err := responseCacheKey(api, req.Method, req)
	if err != nil {
		t.Fatal(err)
	}
	nextAPI := api
	nextAPI.CachePolicyVersion++
	nextKey, err := responseCacheKey(nextAPI, req.Method, req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Del(context.Background(), key, nextKey).Err() })
	if err := client.Del(ctx, key, nextKey).Err(); err != nil {
		t.Fatal(err)
	}

	stored := OpenResult{
		Body:           `{"code":0,"data":{"source":"integration"}}`,
		ContentType:    "application/json",
		ResponseStatus: 200,
		Success:        true,
	}
	service.storeResponseCache(ctx, api, req.Method, req, &stored)
	if !stored.cacheStored || stored.cacheCachedAt.IsZero() || stored.cacheExpiresAt.IsZero() {
		t.Fatalf("successful cache write did not attach metadata: %+v", stored)
	}
	result, hit := service.loadResponseCache(ctx, api, req.Method, req)
	if !hit {
		t.Fatal("response cache round-trip did not produce a hit")
	}
	if result.Body != `{"code":0,"data":{"source":"integration"}}` || result.ResponseStatus != 200 || !result.Success || !result.CacheHit {
		t.Fatalf("unexpected cached result: %+v", result)
	}
	service.applyResponseCacheMetadata(&result)
	var decorated map[string]json.RawMessage
	if err := json.Unmarshal([]byte(result.Body), &decorated); err != nil {
		t.Fatal(err)
	}
	var metadata responseCachePublicMetadata
	if err := json.Unmarshal(decorated[responseCacheMetadataField], &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata.Status != "HIT" || !metadata.Hit || !metadata.Cached || metadata.ExpiresAt == "" || metadata.CachedAt == "" || metadata.RemainingSeconds <= 0 {
		t.Fatalf("unexpected cache metadata: %+v", metadata)
	}
	if ttl, err := client.TTL(ctx, key).Result(); err != nil || ttl <= 0 {
		t.Fatalf("cached response did not receive a positive TTL: ttl=%v err=%v", ttl, err)
	}
	if result, hit := service.loadResponseCache(ctx, nextAPI, req.Method, req); hit {
		t.Fatalf("changed policy version unexpectedly reused old cache entry: %+v", result)
	}
}
