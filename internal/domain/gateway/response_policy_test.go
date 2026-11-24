package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"bugpk-api/internal/pkg/jsoncleaning"

	"bugpk-api/internal/domain/advertising"

	"github.com/redis/go-redis/v9"
)

type responseAdProviderStub struct {
	item advertising.Advertisement
}

// responseCacheRedisHook supplies deterministic GET/DEL replies without
// requiring a process-wide Redis instance. It lets the cache tests exercise
// the actual loadResponseCache path, including the miss cleanup branch.
type responseCacheRedisHook struct {
	value string
	gets  int
	dels  int
}

func (h *responseCacheRedisHook) DialHook(next redis.DialHook) redis.DialHook {
	return next
}

func (h *responseCacheRedisHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		switch strings.ToLower(cmd.Name()) {
		case "get":
			h.gets++
			if value, ok := cmd.(*redis.StringCmd); ok {
				value.SetVal(h.value)
				return nil
			}
		case "del":
			h.dels++
			if value, ok := cmd.(*redis.IntCmd); ok {
				value.SetVal(1)
				return nil
			}
		}
		return next(ctx, cmd)
	}
}

func (h *responseCacheRedisHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}

func (s responseAdProviderStub) ResolveResponseAd(context.Context, int64) (advertising.Advertisement, bool, error) {
	return s.item, true, nil
}

func TestLoadResponseCacheRejectsUnsuccessfulEntryAndFallsBackToUpstream(t *testing.T) {
	now := time.Date(2026, time.August, 10, 0, 30, 0, 0, time.UTC)
	encoded, err := json.Marshal(responseCacheEntry{
		Body:           `{"code":500,"message":"temporary"}`,
		CachedAt:       now.Add(-time.Second),
		ContentType:    "application/json",
		ExpiresAt:      now.Add(time.Minute),
		ResponseStatus: 200,
		Success:        false,
		TTLSeconds:     60,
	})
	if err != nil {
		t.Fatal(err)
	}
	hook := &responseCacheRedisHook{value: string(encoded)}
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})
	client.AddHook(hook)
	t.Cleanup(func() { _ = client.Close() })
	service := &Service{rdb: client, now: func() time.Time { return now }}
	api := InterfaceConfig{ID: 880, RequestURL: "https://upstream.example/api", CacheEnabled: 1}
	result, hit := service.loadResponseCache(context.Background(), api, "GET", OpenRequest{ClientIP: "203.0.113.88"})
	if hit {
		t.Fatalf("unsuccessful cache entry short-circuited polling: %+v", result)
	}
	if hook.gets != 1 || hook.dels != 1 {
		t.Fatalf("invalid cache entry was not read/removed: gets=%d dels=%d", hook.gets, hook.dels)
	}
}

func TestLoadResponseCacheRejectsNonTerminalBusinessCodeUnderPollingCheck(t *testing.T) {
	now := time.Date(2026, time.August, 10, 0, 30, 0, 0, time.UTC)
	encoded, err := json.Marshal(responseCacheEntry{
		Body:           `{"code":500,"message":"temporary"}`,
		CachedAt:       now.Add(-time.Second),
		ContentType:    "application/json",
		ExpiresAt:      now.Add(time.Minute),
		ResponseStatus: 200,
		Success:        true, // stale/legacy entry inconsistent with the body
		TTLSeconds:     60,
	})
	if err != nil {
		t.Fatal(err)
	}
	hook := &responseCacheRedisHook{value: string(encoded)}
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})
	client.AddHook(hook)
	t.Cleanup(func() { _ = client.Close() })
	service := &Service{rdb: client, now: func() time.Time { return now }}
	api := InterfaceConfig{
		ID:                   881,
		RequestURL:           "https://upstream.example/api",
		CacheEnabled:         1,
		PollingEnabled:       1,
		PollingMode:          "PRIMARY",
		PollingCheckEnabled:  1,
		PollingCheckField:    "code",
		PollingCheckExpected: "200",
		PollingStopCode:      "404",
		UpstreamConfigs:      []UpstreamConfig{{URL: "https://backup.example/api"}},
	}
	if result, hit := service.loadResponseCache(context.Background(), api, "GET", OpenRequest{ClientIP: "203.0.113.881"}); hit {
		t.Fatalf("non-terminal business code was served from cache: %+v", result)
	}
	if hook.gets != 1 || hook.dels != 1 {
		t.Fatalf("stale business-code entry was not invalidated: gets=%d dels=%d", hook.gets, hook.dels)
	}
}

func TestLoadResponseCacheRejectsNon2xxEntryEvenWhenSuccessFlagIsTrue(t *testing.T) {
	now := time.Date(2026, time.August, 10, 0, 30, 0, 0, time.UTC)
	encoded, err := json.Marshal(responseCacheEntry{
		Body:           `{"code":500}`,
		CachedAt:       now.Add(-time.Second),
		ContentType:    "application/json",
		ExpiresAt:      now.Add(time.Minute),
		ResponseStatus: 500,
		Success:        true,
		TTLSeconds:     60,
	})
	if err != nil {
		t.Fatal(err)
	}
	hook := &responseCacheRedisHook{value: string(encoded)}
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})
	client.AddHook(hook)
	t.Cleanup(func() { _ = client.Close() })
	service := &Service{rdb: client, now: func() time.Time { return now }}
	api := InterfaceConfig{ID: 882, RequestURL: "https://upstream.example/api", CacheEnabled: 1}
	if result, hit := service.loadResponseCache(context.Background(), api, "GET", OpenRequest{ClientIP: "203.0.113.882"}); hit {
		t.Fatalf("non-2xx cache entry was served: %+v", result)
	}
	if hook.dels != 1 {
		t.Fatalf("non-2xx cache entry was not removed: dels=%d", hook.dels)
	}
}

func TestResponseCachePollingPolicyKeepsLegacyUncheckedPayloads(t *testing.T) {
	now := time.Date(2026, time.August, 10, 0, 30, 0, 0, time.UTC)
	entry := responseCacheEntry{
		Body:           `{"code":500}`,
		CachedAt:       now.Add(-time.Second),
		ExpiresAt:      now.Add(time.Minute),
		ResponseStatus: 200,
		Success:        true,
		TTLSeconds:     60,
	}
	if !responseCacheEntryMatchesPolling(InterfaceConfig{}, entry) {
		t.Fatal("an interface without response checking must keep arbitrary HTTP-2xx payloads cacheable")
	}

	withStop := InterfaceConfig{PollingStopCode: "404", PollingCheckField: "code", PollingCheckExpected: "200"}
	if responseCacheEntryMatchesPolling(withStop, entry) {
		t.Fatal("a configured stop-code policy must reject a non-success business code")
	}
}

func TestResponseCachePollingPolicyRejectsNonTerminalBusinessCodeWithoutExplicitCheck(t *testing.T) {
	api := InterfaceConfig{
		PollingEnabled:  1,
		PollingMode:     "PRIMARY",
		RequestURL:      "https://primary.example/api",
		UpstreamConfigs: []UpstreamConfig{{URL: "https://backup.example/api"}},
	}
	if responseCacheEntryMatchesPolling(api, responseCacheEntry{
		Body:           `{"code":500}`,
		ResponseStatus: http.StatusOK,
		Success:        true,
	}) {
		t.Fatal("polling cache must reject a non-terminal business error code")
	}
	if !responseCacheEntryMatchesPolling(api, responseCacheEntry{
		Body:           `{"code":200}`,
		ResponseStatus: http.StatusOK,
		Success:        true,
	}) {
		t.Fatal("polling cache should keep a successful business code")
	}
}

// A cached response does not carry the candidate node that produced it in
// legacy deployments.  When nodes use different response-check policies we
// therefore must not accept a payload merely because one (possibly more
// permissive) node would accept it: serving that entry could bypass the
// stricter node and hide a required polling attempt.  Unknown-source entries
// are safe only when every effective candidate policy accepts the payload.
func TestResponseCachePollingPolicyRejectsPayloadAcceptedByOnlyOneNodePolicy(t *testing.T) {
	api := InterfaceConfig{
		PollingEnabled:       1,
		PollingMode:          "PRIMARY",
		RequestURL:           "https://primary.example/api",
		PollingCheckEnabled:  1,
		PollingCheckField:    "code",
		PollingCheckExpected: "200",
		UpstreamConfigs: []UpstreamConfig{{
			URL:                  "https://backup.example/api",
			PollingCheckEnabled:  1,
			PollingCheckField:    "code",
			PollingCheckExpected: "500",
		}},
	}
	entry := responseCacheEntry{
		Body:           `{"code":500,"message":"temporary"}`,
		ResponseStatus: http.StatusOK,
		Success:        true,
	}
	if responseCacheEntryMatchesPolling(api, entry) {
		t.Fatal("cache entry accepted by only the permissive backup policy must not bypass the strict primary policy")
	}
}

// Automatic compatibility checks accept legacy success aliases such as
// code=0, while an explicitly enabled code/200 check accepts only code=200.
// They must therefore remain distinct cache policies even though their public
// field and expected value are identical.
func TestResponseCachePollingPolicyDistinguishesAutomaticAndExplicitChecks(t *testing.T) {
	api := InterfaceConfig{
		PollingEnabled:      1,
		PollingMode:         "PRIMARY",
		RequestURL:          "https://primary.example/api",
		PollingCheckEnabled: 0,
		UpstreamConfigs: []UpstreamConfig{{
			URL:                  "https://backup.example/api",
			PollingCheckEnabled:  1,
			PollingCheckField:    "code",
			PollingCheckExpected: "200",
		}},
	}
	entry := responseCacheEntry{
		Body:           `{"code":0,"message":"legacy success"}`,
		ResponseStatus: http.StatusOK,
		Success:        true,
	}
	if responseCacheEntryMatchesPolling(api, entry) {
		t.Fatal("automatic code/200 policy hid the stricter explicit code/200 policy")
	}
}

func TestResponseCacheTTLUsesFirstMatchingRule(t *testing.T) {
	api := InterfaceConfig{CacheEnabled: 1, CacheDefaultTTLSeconds: 5, CacheRules: []ResponseCacheRule{
		{StatusCode: 200, FieldName: "code", Operator: "EQ", ExpectedValue: "0", TTLSeconds: 120},
	}}
	ttl := responseCacheTTL(api, OpenResult{Body: `{"code":0}`, ResponseStatus: 200, Success: true})
	if ttl.Seconds() != 120 {
		t.Fatalf("ttl=%v, want 120s", ttl)
	}
}

func TestResolveDeliveryPolicyUsesIndependentOverrides(t *testing.T) {
	local := deliveryPolicySnapshot{
		CacheEnabled:           0,
		CacheDefaultTTLSeconds: 11,
		CacheRules:             []ResponseCacheRule{{FieldName: "local", TTLSeconds: 11}},
		ResponseAdEnabled:      0,
		ResponseAdField:        "_local_ad",
		ResponseAdID:           17,
		PolicyVersion:          8,
	}
	global := deliveryPolicySnapshot{
		CacheEnabled:           1,
		CacheDefaultTTLSeconds: 90,
		CacheRules:             []ResponseCacheRule{{FieldName: "global", TTLSeconds: 90}},
		ResponseAdEnabled:      1,
		ResponseAdField:        "_global_ad",
		ResponseAdID:           23,
		PolicyVersion:          12,
	}

	cacheInherited := resolveDeliveryPolicy(local, global, 0, 1)
	if cacheInherited.CacheEnabled != 1 || cacheInherited.CacheDefaultTTLSeconds != 90 ||
		cacheInherited.ResponseAdEnabled != 0 || cacheInherited.ResponseAdField != "_local_ad" ||
		cacheInherited.ResponseAdID != 17 || cacheInherited.PolicyVersion != 12 {
		t.Fatalf("cache/ad policies were not resolved independently: %+v", cacheInherited)
	}

	adInherited := resolveDeliveryPolicy(local, global, 1, 0)
	if adInherited.CacheEnabled != 0 || adInherited.CacheDefaultTTLSeconds != 11 ||
		adInherited.ResponseAdEnabled != 1 || adInherited.ResponseAdField != "_global_ad" ||
		adInherited.ResponseAdID != 23 || adInherited.PolicyVersion != 8 {
		t.Fatalf("cache override or ad inheritance failed: %+v", adInherited)
	}
}

func TestResolveDeliveryPolicyUsesGlobalPolicyWhenBothStrategiesInherit(t *testing.T) {
	local := deliveryPolicySnapshot{
		CacheEnabled: 0, CacheDefaultTTLSeconds: 11,
		CacheRules:        []ResponseCacheRule{{FieldName: "local", TTLSeconds: 11}},
		ResponseAdEnabled: 0, ResponseAdField: "_local_ad", ResponseAdID: 17, PolicyVersion: 8,
	}
	global := deliveryPolicySnapshot{
		CacheEnabled: 1, CacheDefaultTTLSeconds: 90,
		CacheRules:        []ResponseCacheRule{{FieldName: "global", TTLSeconds: 90}},
		ResponseAdEnabled: 1, ResponseAdField: "_global_ad", ResponseAdID: 23, PolicyVersion: 12,
	}

	effective := resolveDeliveryPolicy(local, global, 0, 0)
	if effective.CacheEnabled != 1 || effective.CacheDefaultTTLSeconds != 90 ||
		len(effective.CacheRules) != 1 || effective.CacheRules[0].FieldName != "global" ||
		effective.ResponseAdEnabled != 1 || effective.ResponseAdField != "_global_ad" ||
		effective.ResponseAdID != 23 || effective.PolicyVersion != 12 {
		t.Fatalf("global policy was not inherited for both strategies: %+v", effective)
	}
}

func TestResolveDeliveryPolicyUsesInterfacePolicyWhenBothStrategiesOverride(t *testing.T) {
	local := deliveryPolicySnapshot{
		CacheEnabled: 1, CacheDefaultTTLSeconds: 11,
		CacheRules:        []ResponseCacheRule{{FieldName: "local", TTLSeconds: 11}},
		ResponseAdEnabled: 1, ResponseAdField: "_local_ad", ResponseAdID: 17, PolicyVersion: 8,
	}
	global := deliveryPolicySnapshot{
		CacheEnabled: 0, CacheDefaultTTLSeconds: 90,
		CacheRules:        []ResponseCacheRule{{FieldName: "global", TTLSeconds: 90}},
		ResponseAdEnabled: 0, ResponseAdField: "_global_ad", ResponseAdID: 23, PolicyVersion: 12,
	}

	effective := resolveDeliveryPolicy(local, global, 1, 1)
	if effective.CacheEnabled != 1 || effective.CacheDefaultTTLSeconds != 11 ||
		len(effective.CacheRules) != 1 || effective.CacheRules[0].FieldName != "local" ||
		effective.ResponseAdEnabled != 1 || effective.ResponseAdField != "_local_ad" ||
		effective.ResponseAdID != 17 || effective.PolicyVersion != 8 {
		t.Fatalf("interface policy did not override both global strategies: %+v", effective)
	}
}

func TestResolveDeliveryPolicyAllowsInterfaceDisableToOverrideGlobalEnable(t *testing.T) {
	effective := resolveDeliveryPolicy(
		deliveryPolicySnapshot{CacheEnabled: 0, CacheDefaultTTLSeconds: 0, PolicyVersion: 4},
		deliveryPolicySnapshot{CacheEnabled: 1, CacheDefaultTTLSeconds: 120, PolicyVersion: 9},
		1, 0,
	)
	if effective.CacheEnabled != 0 || effective.CacheDefaultTTLSeconds != 0 || effective.PolicyVersion != 4 {
		t.Fatalf("interface cache disable did not win: %+v", effective)
	}
}

func TestResolveDeliveryPolicyUsesInterfaceRequestTimeoutBeforeGlobal(t *testing.T) {
	local := deliveryPolicySnapshot{RequestTimeoutMs: 2500, RequestTimeoutOverride: 1}
	global := deliveryPolicySnapshot{RequestTimeoutMs: 15000}
	effective := resolveDeliveryPolicy(local, global, 0, 0)
	if effective.RequestTimeoutMs != 2500 {
		t.Fatalf("interface timeout did not override global: %d", effective.RequestTimeoutMs)
	}
}

func TestResolveDeliveryPolicyInheritsGlobalRequestTimeoutWhenLocalIsUnset(t *testing.T) {
	local := deliveryPolicySnapshot{RequestTimeoutMs: 0, RequestTimeoutOverride: 0}
	global := deliveryPolicySnapshot{RequestTimeoutMs: 12000}
	effective := resolveDeliveryPolicy(local, global, 0, 0)
	if effective.RequestTimeoutMs != 12000 {
		t.Fatalf("unset interface timeout = %d, want 12000", effective.RequestTimeoutMs)
	}
}

func TestResponseCacheKeyChangesWhenDeliveryPolicyVersionChanges(t *testing.T) {
	api := InterfaceConfig{ID: 1, RequestURL: "https://example.com/api", CacheEnabled: 1, CachePolicyVersion: 1}
	first, err := responseCacheKey(api, "GET", OpenRequest{ClientIP: "203.0.113.1"})
	if err != nil {
		t.Fatal(err)
	}
	api.CachePolicyVersion = 2
	second, err := responseCacheKey(api, "GET", OpenRequest{ClientIP: "203.0.113.1"})
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("delivery policy version must invalidate the old response-cache key")
	}
}

func TestResponseCacheKeyExcludesAPIKeyButIncludesClientIsolation(t *testing.T) {
	api := InterfaceConfig{ID: 1, RequestURL: "https://example.com/api", CacheEnabled: 1}
	first, err := responseCacheKey(api, "GET", OpenRequest{ClientIP: "203.0.113.1", Query: map[string][]string{"key": {"secret-a"}, "q": {"x"}}})
	if err != nil {
		t.Fatal(err)
	}
	second, _ := responseCacheKey(api, "GET", OpenRequest{ClientIP: "203.0.113.1", Query: map[string][]string{"key": {"secret-b"}, "q": {"x"}}})
	third, _ := responseCacheKey(api, "GET", OpenRequest{ClientIP: "203.0.113.2", Query: map[string][]string{"key": {"secret-b"}, "q": {"x"}}})
	if first != second {
		t.Fatal("API key must not fragment or leak into response cache keys")
	}
	if first == third {
		t.Fatal("different client IPs must remain isolated")
	}
}

func TestResponseCacheKeyIgnoresDynamicRequestHeaders(t *testing.T) {
	api := InterfaceConfig{ID: 2, RequestURL: "https://example.com/api", CacheEnabled: 1}
	base := OpenRequest{
		ClientIP: "203.0.113.2",
		Headers: map[string]string{
			"Accept":          "application/json",
			"Content-Type":    "application/json",
			"X-Tenant-ID":     "tenant-a",
			"User-Agent":      "Mozilla/5.0",
			"Origin":          "https://client-a.example",
			"Referer":         "https://client-a.example/page",
			"X-Request-ID":    "request-one",
			"traceparent":     "00-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bbbbbbbbbbbbbbbbb-01",
			"sec-fetch-site":  "same-origin",
			"Accept-Encoding": "gzip, br",
		},
	}
	first, err := responseCacheKey(api, "GET", base)
	if err != nil {
		t.Fatal(err)
	}
	base.Headers["User-Agent"] = "curl/8"
	base.Headers["Origin"] = "https://another-client.example"
	base.Headers["Referer"] = "https://another-client.example/other"
	base.Headers["X-Request-ID"] = "request-two"
	base.Headers["traceparent"] = "00-cccccccccccccccccccccccccccccccc-dddddddddddddddd-01"
	base.Headers["sec-fetch-site"] = "cross-site"
	base.Headers["Accept-Encoding"] = "identity"
	second, err := responseCacheKey(api, "GET", base)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("dynamic browser/tracing headers must not fragment the response cache key")
	}

	base.Headers["Accept"] = "text/plain"
	third, err := responseCacheKey(api, "GET", base)
	if err != nil {
		t.Fatal(err)
	}
	if second == third {
		t.Fatal("Accept must remain part of the response cache key")
	}
	base.Headers["Accept"] = "application/json"
	base.Headers["X-Tenant-ID"] = "tenant-b"
	fourth, err := responseCacheKey(api, "GET", base)
	if err != nil {
		t.Fatal(err)
	}
	if first == fourth {
		t.Fatal("stable business headers must remain part of the response cache key")
	}
}

func TestResponseCacheKeyIgnoresUpstreamHealthMetadata(t *testing.T) {
	api := InterfaceConfig{
		ID:             3,
		RequestURL:     "https://primary.example/api",
		CacheEnabled:   1,
		PollingEnabled: 1,
		PollingMode:    "FASTEST",
		UpstreamConfigs: []UpstreamConfig{{
			URL:                  "https://backup.example/api",
			PollingCheckEnabled:  1,
			PollingCheckField:    "code",
			PollingCheckExpected: "200",
			HealthStatus:         "healthy",
			FailureCount:         0,
			LastCheckedAt:        "2026-08-08T10:00:00Z",
			LastLatencyMs:        12,
		}},
	}
	first, err := responseCacheKey(api, "GET", OpenRequest{ClientIP: "203.0.113.3"})
	if err != nil {
		t.Fatal(err)
	}
	api.UpstreamConfigs[0].HealthStatus = "degraded"
	api.UpstreamConfigs[0].FailureCount = 4
	api.UpstreamConfigs[0].LastCheckedAt = "2026-08-08T10:01:00Z"
	api.UpstreamConfigs[0].LastLatencyMs = 900
	second, err := responseCacheKey(api, "GET", OpenRequest{ClientIP: "203.0.113.3"})
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("health display metadata must not invalidate a response cache entry")
	}

	api.UpstreamConfigs[0].URL = "https://new-backup.example/api"
	third, err := responseCacheKey(api, "GET", OpenRequest{ClientIP: "203.0.113.3"})
	if err != nil {
		t.Fatal(err)
	}
	if second == third {
		t.Fatal("a changed upstream URL must invalidate the response cache key")
	}
}

func TestResponseCacheKeyIncludesResponseFieldMappings(t *testing.T) {
	api := InterfaceConfig{
		ID:                      31,
		RequestURL:              "https://primary.example/api",
		CacheEnabled:            1,
		ResponseCleaningEnabled: 1,
		ResponseFieldMappings:   []jsoncleaning.FieldMapping{{Source: "payload", Target: "data"}},
		UpstreamConfigs: []UpstreamConfig{{
			URL:                   "https://backup.example/api",
			ResponseFieldMappings: []jsoncleaning.FieldMapping{{Source: "media_link", Target: "url"}},
		}},
	}
	first, err := responseCacheKey(api, "GET", OpenRequest{})
	if err != nil {
		t.Fatal(err)
	}
	api.ResponseFieldMappings[0].Source = "result"
	second, err := responseCacheKey(api, "GET", OpenRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("changed primary mapping did not invalidate response cache key")
	}
	api.ResponseFieldMappings[0].Source = "payload"
	api.UpstreamConfigs[0].ResponseFieldMappings[0].Source = "play_link"
	third, err := responseCacheKey(api, "GET", OpenRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if first == third {
		t.Fatal("changed upstream mapping did not invalidate response cache key")
	}
}

func TestResponseCacheKeyIncludesBasePollingValidation(t *testing.T) {
	api := InterfaceConfig{
		ID:                   4,
		RequestURL:           "https://example.com/api",
		CacheEnabled:         1,
		PollingEnabled:       1,
		PollingMode:          "PRIMARY",
		PollingCheckEnabled:  1,
		PollingCheckField:    "code",
		PollingCheckExpected: "200",
	}
	first, err := responseCacheKey(api, "GET", OpenRequest{ClientIP: "203.0.113.4"})
	if err != nil {
		t.Fatal(err)
	}
	api.PollingCheckExpected = "0"
	second, err := responseCacheKey(api, "GET", OpenRequest{ClientIP: "203.0.113.4"})
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("base upstream validation changes must invalidate the response cache key")
	}
}

func TestResponseCacheKeySeparatesInferredPollingFromExplicitSingle(t *testing.T) {
	api := InterfaceConfig{
		ID:              405,
		RequestURL:      "https://primary.example/api",
		CacheEnabled:    1,
		PollingEnabled:  0,
		PollingMode:     "",
		UpstreamConfigs: []UpstreamConfig{{URL: "https://backup.example/api"}},
	}
	inferredPollingKey, err := responseCacheKey(api, "GET", OpenRequest{ClientIP: "203.0.113.405"})
	if err != nil {
		t.Fatal(err)
	}

	api.PollingMode = "SINGLE"
	explicitSingleKey, err := responseCacheKey(api, "GET", OpenRequest{ClientIP: "203.0.113.405"})
	if err != nil {
		t.Fatal(err)
	}
	if inferredPollingKey == explicitSingleKey {
		t.Fatal("legacy inferred polling and explicit SINGLE shared a response cache key")
	}
}

func TestResponseCacheKeyIncludesPollingStopCode(t *testing.T) {
	api := InterfaceConfig{
		ID:                   404,
		RequestURL:           "https://example.com/api",
		CacheEnabled:         1,
		PollingEnabled:       1,
		PollingMode:          "PRIMARY",
		PollingCheckEnabled:  1,
		PollingCheckField:    "code",
		PollingCheckExpected: "200",
		PollingStopCode:      "404",
	}
	first, err := responseCacheKey(api, "GET", OpenRequest{ClientIP: "203.0.113.404"})
	if err != nil {
		t.Fatal(err)
	}
	api.PollingStopCode = "40401"
	second, err := responseCacheKey(api, "GET", OpenRequest{ClientIP: "203.0.113.404"})
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("polling stop-code changes must invalidate the response cache key")
	}
}

func TestResponseCacheKeyChangesWhenResponseCleaningChanges(t *testing.T) {
	api := InterfaceConfig{ID: 41, RequestURL: "https://example.com/api", CacheEnabled: 1}
	first, err := responseCacheKey(api, "GET", OpenRequest{ClientIP: "203.0.113.41"})
	if err != nil {
		t.Fatal(err)
	}
	api.ResponseCleaningEnabled = 1
	second, err := responseCacheKey(api, "GET", OpenRequest{ClientIP: "203.0.113.41"})
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("response cleaning switch must isolate cached representations")
	}
}

func TestResponseCacheKeyUsesEffectiveDefaultPostContentType(t *testing.T) {
	api := InterfaceConfig{ID: 5, RequestURL: "https://example.com/api", CacheEnabled: 1}
	request := OpenRequest{ClientIP: "203.0.113.5"}
	withoutHeader, err := responseCacheKey(api, "POST", request)
	if err != nil {
		t.Fatal(err)
	}
	request.Headers = map[string]string{"Content-Type": "application/json"}
	request.Body = []byte(`{}`)
	withHeader, err := responseCacheKey(api, "POST", request)
	if err != nil {
		t.Fatal(err)
	}
	if withoutHeader != withHeader {
		t.Fatal("omitted POST Content-Type must use the same key as invokeOne's JSON default")
	}
}

func TestResponseCacheWriteContextSurvivesParentCancellation(t *testing.T) {
	parent, cancelParent := context.WithCancel(context.WithValue(context.Background(), "cache-test", "value"))
	writeContext, cancelWrite := responseCacheWriteContext(parent)
	defer cancelWrite()
	cancelParent()
	select {
	case <-writeContext.Done():
		t.Fatal("response-cache write context was canceled with the client request")
	default:
	}
	if got := writeContext.Value("cache-test"); got != "value" {
		t.Fatalf("write context lost request values: %v", got)
	}
}

func TestApplyResponseCacheMetadataRequiresConfirmedCacheAndOverridesForgery(t *testing.T) {
	now := time.Date(2026, time.August, 10, 0, 30, 0, 0, time.UTC)
	service := &Service{now: func() time.Time { return now }}
	uncached := OpenResult{Body: `{"code":0}`}
	service.applyResponseCacheMetadata(&uncached)
	if uncached.Body != `{"code":0}` {
		t.Fatalf("uncached response was decorated: %s", uncached.Body)
	}

	stored := OpenResult{
		Body:            `{"code":0,"_bugpkCache":{"status":"FORGED"}}`,
		cacheCachedAt:   now.Add(-5 * time.Second),
		cacheExpiresAt:  now.Add(55 * time.Second),
		cacheStored:     true,
		cacheTTLSeconds: 60,
	}
	service.applyResponseCacheMetadata(&stored)
	var body map[string]json.RawMessage
	if err := json.Unmarshal([]byte(stored.Body), &body); err != nil {
		t.Fatal(err)
	}
	var metadata responseCachePublicMetadata
	if err := json.Unmarshal(body[responseCacheMetadataField], &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata.Status != "STORED" || metadata.Hit || !metadata.Cached || metadata.TTLSeconds != 60 || metadata.RemainingSeconds != 55 {
		t.Fatalf("unexpected stored metadata: %+v", metadata)
	}
	if metadata.CachedAt != "2026-08-10T00:29:55Z" || metadata.ExpiresAt != "2026-08-10T00:30:55Z" {
		t.Fatalf("unexpected cache timestamps: %+v", metadata)
	}

	hit := OpenResult{
		Body:            `{"code":0}`,
		CacheHit:        true,
		cacheCachedAt:   now.Add(-5 * time.Second),
		cacheExpiresAt:  now.Add(55 * time.Second),
		cacheTTLSeconds: 60,
	}
	service.applyResponseCacheMetadata(&hit)
	body = nil
	if err := json.Unmarshal([]byte(hit.Body), &body); err != nil {
		t.Fatal(err)
	}
	metadata = responseCachePublicMetadata{}
	if err := json.Unmarshal(body[responseCacheMetadataField], &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata.Status != "HIT" || !metadata.Hit || metadata.RemainingSeconds != 55 {
		t.Fatalf("unexpected hit metadata: %+v", metadata)
	}
}

func TestApplyResponseAdvertisementInjectsOnlyNewJSONField(t *testing.T) {
	service := &Service{responseAdProvider: responseAdProviderStub{item: advertising.Advertisement{ID: "8", Title: "活动", LinkURL: "https://example.com/ad", LinkTarget: "_blank"}}}
	result := OpenResult{Body: `{"code":0,"data":{"ok":true}}`, ContentType: "application/json", ResponseStatus: 200, Success: true}
	service.applyResponseAdvertisement(context.Background(), InterfaceConfig{ID: 1, ResponseAdEnabled: 1, ResponseAdField: "_ad"}, &result)
	if result.Body == `{"code":0,"data":{"ok":true}}` || result.ResponseSize == 0 {
		t.Fatalf("advertisement was not injected: %s", result.Body)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(result.Body), &body); err != nil {
		t.Fatal(err)
	}
	ad, ok := body["_ad"].(map[string]any)
	if !ok || ad["name"] != "活动" || ad["url"] != "https://example.com/ad" {
		t.Fatalf("public advertisement aliases missing: %#v", body["_ad"])
	}
	original := result.Body
	service.applyResponseAdvertisement(context.Background(), InterfaceConfig{ID: 1, ResponseAdEnabled: 1, ResponseAdField: "_ad"}, &result)
	if result.Body != original {
		t.Fatal("existing response field must not be overwritten")
	}

	orderedService := &Service{
		now:                func() time.Time { return time.Date(2026, time.August, 10, 0, 30, 0, 0, time.UTC) },
		responseAdProvider: responseAdProviderStub{item: advertising.Advertisement{ID: "8", Title: "ad", LinkURL: "https://example.com/ad"}},
	}
	ordered := OpenResult{
		Body:            `{"z":1,"data":{"b":2,"a":1}}`,
		Success:         true,
		cacheCachedAt:   time.Date(2026, time.August, 10, 0, 29, 0, 0, time.UTC),
		cacheExpiresAt:  time.Date(2026, time.August, 10, 0, 31, 0, 0, time.UTC),
		cacheStored:     true,
		cacheTTLSeconds: 120,
	}
	orderedService.applyResponseCacheMetadata(&ordered)
	orderedService.applyResponseAdvertisement(context.Background(), InterfaceConfig{ID: 1, ResponseAdEnabled: 1, ResponseAdField: "_advertisement"}, &ordered)
	for _, key := range []string{`"z"`, `"data"`, `"_bugpkCache"`, `"_advertisement"`} {
		if strings.Count(ordered.Body, key) != 1 {
			t.Fatalf("field %s count in ordered response = %d; body=%s", key, strings.Count(ordered.Body, key), ordered.Body)
		}
	}
	positions := []int{
		strings.Index(ordered.Body, `"z"`),
		strings.Index(ordered.Body, `"data"`),
		strings.Index(ordered.Body, `"_bugpkCache"`),
		strings.Index(ordered.Body, `"_advertisement"`),
	}
	for i := 1; i < len(positions); i++ {
		if positions[i-1] >= positions[i] {
			t.Fatalf("response field order = %v; body=%s", positions, ordered.Body)
		}
	}
}

func TestResponseCacheRedisFailureFailsOpen(t *testing.T) {
	client := redis.NewClient(&redis.Options{
		Addr:         "127.0.0.1:1",
		DialTimeout:  20 * time.Millisecond,
		MaxRetries:   -1,
		ReadTimeout:  20 * time.Millisecond,
		WriteTimeout: 20 * time.Millisecond,
	})
	t.Cleanup(func() { _ = client.Close() })

	service := &Service{rdb: client}
	api := InterfaceConfig{
		CacheDefaultTTLSeconds: 60,
		CacheEnabled:           1,
		ID:                     73,
		RequestURL:             "https://upstream.example.test/cache",
	}
	req := OpenRequest{
		ClientIP: "203.0.113.73",
		Method:   "GET",
		Query:    map[string][]string{"probe": {"redis-fail-open"}},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()

	if result, ok := service.loadResponseCache(ctx, api, "GET", req); ok {
		t.Fatalf("unavailable Redis unexpectedly returned a cache hit: %+v", result)
	}

	// Cache persistence is an optimization. An unavailable Redis instance must
	// not turn a successful upstream response into an API failure or panic.
	result := OpenResult{
		Body:           `{"code":0,"data":{"ok":true}}`,
		ContentType:    "application/json",
		ResponseStatus: 200,
		Success:        true,
	}
	service.storeResponseCache(ctx, api, "GET", req, &result)
	if result.cacheStored {
		t.Fatal("failed Redis write incorrectly marked the response as cached")
	}
}
