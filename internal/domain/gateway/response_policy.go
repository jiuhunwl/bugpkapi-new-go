package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"bugpk-api/internal/domain/advertising"
	"bugpk-api/internal/pkg/decimalvalue"
	"bugpk-api/internal/pkg/domainrouting"
	"bugpk-api/internal/pkg/httpstatus"
	"bugpk-api/internal/pkg/jsonappend"
	"bugpk-api/internal/pkg/jsoncleaning"
	"bugpk-api/internal/pkg/jsonpathvalue"
	"bugpk-api/internal/pkg/masking"
	"bugpk-api/internal/pkg/requesttimeout"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

const (
	maxGatewayCacheTTL     = 24 * time.Hour
	maxCachedResponseBytes = 2 << 20
	// v6 stores the automatic business-code polling policy introduced for
	// legacy polling rows that had response checking disabled. Entries from
	// older versions are intentionally isolated so a stale 2xx/code=500 value
	// can never bypass a live fallback attempt.
	// v5 stores authoritative cache timestamps alongside the cached body and
	// invalidates entries created before polling stop-code/inheritance fixes.
	// v4 stores authoritative cache timestamps alongside the cached body so
	// every JSON response can expose accurate cache metadata. v3 also includes
	// per-upstream response field mappings so changing a
	// cleaning rule can never reuse a response produced by the previous rule.
	// v2 removed request/health metadata that changes between otherwise
	// identical calls from the cache key. Keep the version in the key so old
	// entries can expire naturally without ever being mixed with the new
	// canonical representation.
	responseCacheKeyVersion    = "v6"
	responseCacheMetadataField = "_bugpkCache"
	responseCacheReadTimeout   = 200 * time.Millisecond
	responseCacheWriteTimeout  = 500 * time.Millisecond
)

type responseCacheUpstream struct {
	URL                   string                      `json:"url"`
	PollingCheckEnabled   int                         `json:"pollingCheckEnabled"`
	PollingCheckExpected  string                      `json:"pollingCheckExpected"`
	PollingCheckField     string                      `json:"pollingCheckField"`
	ResponseFieldMappings []jsoncleaning.FieldMapping `json:"responseFieldMappings,omitempty"`
}

// responseCacheDomainRouting contains only routing/polling policy that can
// change which upstream response is returned.  Display labels and health
// telemetry are deliberately omitted: changing a route name or a node's
// last-latency value must not invalidate an otherwise identical response.
type responseCacheDomainRouting struct {
	Enabled        int                        `json:"enabled"`
	Fallback       string                     `json:"fallback"`
	DefaultRouteID string                     `json:"defaultRouteId,omitempty"`
	Routes         []responseCacheDomainRoute `json:"routes,omitempty"`
}

type responseCacheDomainRoute struct {
	ID                      string                      `json:"id"`
	Enabled                 int                         `json:"enabled"`
	Priority                int                         `json:"priority"`
	Match                   domainrouting.MatchConfig   `json:"match"`
	RequestURL              string                      `json:"requestUrl"`
	PollingEnabled          int                         `json:"pollingEnabled"`
	PollingMode             string                      `json:"pollingMode"`
	PollingCheckEnabled     int                         `json:"pollingCheckEnabled"`
	PollingCheckExpected    string                      `json:"pollingCheckExpected"`
	PollingCheckField       string                      `json:"pollingCheckField"`
	PollingStopCode         string                      `json:"pollingStopCode"`
	ResponseCleaningEnabled int                         `json:"responseCleaningEnabled"`
	ResponseFieldMappings   []jsoncleaning.FieldMapping `json:"responseFieldMappings,omitempty"`
	UpstreamConfigs         []responseCacheUpstream     `json:"upstreamConfigs,omitempty"`
}

var responseCacheIgnoredHeaderPrefixes = [...]string{
	"proxy-", "x-forwarded-", "x-envoy-", "x-b3-", "x-request-", "x-correlation-",
	"x-trace-", "x-debug-", "x-log-", "x-amzn-", "x-akamai-", "x-cache-", "x-vercel-",
	"x-middleware-", "x-cloud-trace-", "x-datadog-", "x-ot-", "uber-trace-", "grpc-trace-", "sec-", "cf-",
}

type deliveryPolicySnapshot struct {
	CacheDefaultTTLSeconds int
	CacheEnabled           int
	CacheRules             []ResponseCacheRule
	RequestTimeoutMs       int
	RequestTimeoutOverride int
	ResponseAdEnabled      int
	ResponseAdField        string
	ResponseAdID           int64
	PolicyVersion          int64
}

func defaultDeliveryPolicySnapshot() deliveryPolicySnapshot {
	return deliveryPolicySnapshot{
		ResponseAdField: "_advertisement",
		PolicyVersion:   1,
	}
}

// resolveDeliveryPolicy keeps cache and advertisement inheritance independent.
// An interface may therefore inherit the global cache while selecting its own
// advertisement policy, or do the inverse.
func resolveDeliveryPolicy(local, global deliveryPolicySnapshot, cacheOverride, adOverride int) deliveryPolicySnapshot {
	effective := defaultDeliveryPolicySnapshot()
	if cacheOverride == 1 {
		effective.CacheEnabled = local.CacheEnabled
		effective.CacheDefaultTTLSeconds = local.CacheDefaultTTLSeconds
		effective.CacheRules = cloneResponseCacheRules(local.CacheRules)
		effective.PolicyVersion = normalizedPolicyVersion(local.PolicyVersion)
	} else {
		effective.CacheEnabled = global.CacheEnabled
		effective.CacheDefaultTTLSeconds = global.CacheDefaultTTLSeconds
		effective.CacheRules = cloneResponseCacheRules(global.CacheRules)
		effective.PolicyVersion = normalizedPolicyVersion(global.PolicyVersion)
	}
	if adOverride == 1 {
		effective.ResponseAdEnabled = local.ResponseAdEnabled
		effective.ResponseAdField = normalizedResponseAdField(local.ResponseAdField)
		effective.ResponseAdID = local.ResponseAdID
	} else {
		effective.ResponseAdEnabled = global.ResponseAdEnabled
		effective.ResponseAdField = normalizedResponseAdField(global.ResponseAdField)
		effective.ResponseAdID = global.ResponseAdID
	}
	// Request timeout has its own inheritance switch. A zero local value is an
	// explicit inherit marker even when an old row carried an invalid override.
	effective.RequestTimeoutMs = requesttimeout.ResolveMilliseconds(
		local.RequestTimeoutMs,
		global.RequestTimeoutMs,
		local.RequestTimeoutOverride,
	)
	return effective
}

func cloneResponseCacheRules(rules []ResponseCacheRule) []ResponseCacheRule {
	if len(rules) == 0 {
		return nil
	}
	return append([]ResponseCacheRule(nil), rules...)
}

func normalizedPolicyVersion(version int64) int64 {
	if version < 1 {
		return 1
	}
	return version
}

func normalizedResponseAdField(field string) string {
	field = strings.TrimSpace(field)
	if field == "" {
		return "_advertisement"
	}
	return field
}

type ResponseAdvertisementProvider interface {
	ResolveResponseAd(context.Context, int64) (advertising.Advertisement, bool, error)
}

type responseCacheEntry struct {
	Body           string    `json:"body"`
	CachedAt       time.Time `json:"cachedAt"`
	ContentType    string    `json:"contentType"`
	ExpiresAt      time.Time `json:"expiresAt"`
	ResponseStatus int       `json:"responseStatus"`
	Success        bool      `json:"success"`
	TTLSeconds     int64     `json:"ttlSeconds"`
}

type responseCachePublicMetadata struct {
	Cached           bool   `json:"cached"`
	CachedAt         string `json:"cachedAt"`
	ExpiresAt        string `json:"expiresAt"`
	Hit              bool   `json:"hit"`
	RemainingSeconds int64  `json:"remainingSeconds"`
	Status           string `json:"status"`
	TTLSeconds       int64  `json:"ttlSeconds"`
}

func (s *Service) loadResponseCache(ctx context.Context, api InterfaceConfig, method string, req OpenRequest) (OpenResult, bool) {
	if s.rdb == nil || api.CacheEnabled != 1 {
		return OpenResult{}, false
	}
	key, err := responseCacheKey(api, method, req)
	if err != nil {
		s.warnDeliveryPolicy("build response cache key", api.ID, err)
		return OpenResult{}, false
	}
	readCtx, cancel := responseCacheReadContext(ctx)
	defer cancel()
	data, err := s.rdb.Get(readCtx, key).Bytes()
	if err != nil {
		// redis.Nil is the normal miss path; other errors are useful when
		// diagnosing a cache that was enabled but is not being used.
		if err != redis.Nil {
			s.warnDeliveryPolicy("load response cache", api.ID, err)
		}
		return OpenResult{}, false
	}
	var entry responseCacheEntry
	now := s.currentTime().UTC()
	if json.Unmarshal(data, &entry) != nil ||
		!entry.Success || !httpstatus.IsSuccess(entry.ResponseStatus) ||
		entry.CachedAt.IsZero() || entry.ExpiresAt.IsZero() ||
		!entry.ExpiresAt.After(entry.CachedAt) || !entry.ExpiresAt.After(now) ||
		entry.TTLSeconds <= 0 || !responseCacheEntryMatchesPolling(api, entry) {
		_ = s.rdb.Del(readCtx, key).Err()
		return OpenResult{}, false
	}
	return OpenResult{
		Body:            entry.Body,
		CacheHit:        true,
		CallLogEligible: true,
		ContentType:     entry.ContentType,
		ResponseSize:    int64(len(entry.Body)),
		ResponseStatus:  entry.ResponseStatus,
		Success:         entry.Success,
		cacheCachedAt:   entry.CachedAt.UTC(),
		cacheExpiresAt:  entry.ExpiresAt.UTC(),
		cacheTTLSeconds: entry.TTLSeconds,
	}, true
}

// responseCacheEntryMatchesPolling prevents a stale or manually-written
// response from bypassing the same business-response policy used by live
// upstream polling. Normally storeResponseCache only writes a successful
// result, but older deployments and operational Redis restores can leave an
// entry whose Success flag/body no longer agree with the current polling
// configuration. A cache hit must never turn a non-terminal business code
// (for example code=500 while code=200 is expected) into a successful
// short-circuit.
func responseCacheEntryMatchesPolling(api InterfaceConfig, entry responseCacheEntry) bool {
	if !entry.Success || !httpstatus.IsSuccess(entry.ResponseStatus) {
		return false
	}
	result := OpenResult{Body: entry.Body, ResponseStatus: entry.ResponseStatus}
	// A terminal stop response is intentionally never cached. Rejecting it
	// here also protects against entries written by an older implementation
	// that forgot to clear Success before persisting.
	if shouldStopPolling(api, &result) {
		return false
	}

	configs := upstreamCandidates(api)
	// Most interfaces use one global field/expected pair for every node. Avoid
	// decoding a potentially large cached body once per candidate; only distinct
	// response-check policies need to be evaluated.  A cache entry historically
	// does not identify which candidate produced it, so all distinct policies
	// must accept the body.  Returning true on the first matching policy would
	// let a permissive backup node cache (and later serve) a payload rejected by
	// the strict primary node, effectively bypassing the polling contract.
	seenPolicies := make(map[string]struct{}, len(configs))
	for _, config := range configs {
		policy := effectivePollingCheck(api, config)
		if policy.PollingCheckEnabled != 1 {
			continue
		}
		field := defaultText(strings.TrimSpace(policy.PollingCheckField), "code")
		expected := defaultText(strings.TrimSpace(policy.PollingCheckExpected), "200")
		signature := field + "\x00" + expected + "\x00explicit"
		if policy.pollingCheckAuto {
			signature = field + "\x00" + expected + "\x00automatic"
		}
		if _, exists := seenPolicies[signature]; exists {
			continue
		}
		seenPolicies[signature] = struct{}{}
		if !responsePasses(policy, entry.ResponseStatus, entry.Body) {
			return false
		}
	}
	// With no response check configured on a non-polling interface, HTTP success
	// remains the cacheability criterion; arbitrary JSON envelopes are valid
	// payloads and must not be rejected just because they contain a business-
	// looking `code` field. Polling-enabled rows are marked by
	// effectivePollingCheck and are evaluated above.
	// Reaching this point means every distinct configured policy passed (or no
	// policy was configured), so the entry is safe to use.
	return true
}

func (s *Service) storeResponseCache(ctx context.Context, api InterfaceConfig, method string, req OpenRequest, result *OpenResult) {
	if result == nil || s.rdb == nil || api.CacheEnabled != 1 || !result.Success || len(result.Body) > maxCachedResponseBytes {
		return
	}
	// Re-check the live result against the same polling policy used on cache
	// reads.  Normally result.Success is set only after prepareOpenResponse,
	// but keeping this guard here protects against legacy callers, future code
	// paths, and manually assembled results accidentally persisting a 2xx
	// envelope such as {"code":500} that would otherwise be served as a
	// successful cache hit.
	if !responseCacheEntryMatchesPolling(api, responseCacheEntry{
		Body:           result.Body,
		ResponseStatus: result.ResponseStatus,
		Success:        result.Success,
	}) {
		return
	}
	ttl := responseCacheTTL(api, *result)
	if ttl <= 0 {
		return
	}
	key, err := responseCacheKey(api, method, req)
	if err != nil {
		s.warnDeliveryPolicy("build response cache key", api.ID, err)
		return
	}
	cachedAt := s.currentTime().UTC()
	expiresAt := cachedAt.Add(ttl)
	ttlSeconds := int64(ttl / time.Second)
	data, err := json.Marshal(responseCacheEntry{
		Body: result.Body, CachedAt: cachedAt, ContentType: result.ContentType,
		ExpiresAt: expiresAt, ResponseStatus: result.ResponseStatus,
		Success: result.Success, TTLSeconds: ttlSeconds,
	})
	if err != nil {
		return
	}
	writeCtx, cancel := responseCacheWriteContext(ctx)
	defer cancel()
	if err := s.rdb.Set(writeCtx, key, data, ttl).Err(); err != nil {
		s.warnDeliveryPolicy("store response cache", api.ID, err)
		return
	}
	result.cacheCachedAt = cachedAt
	result.cacheExpiresAt = expiresAt
	result.cacheStored = true
	result.cacheTTLSeconds = ttlSeconds
}

// applyResponseCacheMetadata decorates JSON object responses only after Redis
// has confirmed a cache write or returned a cache hit. Cached bodies remain
// undecorated so remainingSeconds and HIT/STORED are calculated per response.
func (s *Service) applyResponseCacheMetadata(result *OpenResult) {
	if result == nil || (!result.CacheHit && !result.cacheStored) ||
		result.cacheCachedAt.IsZero() || result.cacheExpiresAt.IsZero() ||
		result.cacheTTLSeconds <= 0 {
		return
	}
	status := "STORED"
	if result.CacheHit {
		status = "HIT"
	}
	metadata, err := json.Marshal(responseCachePublicMetadata{
		Cached:           true,
		CachedAt:         result.cacheCachedAt.UTC().Format(time.RFC3339Nano),
		ExpiresAt:        result.cacheExpiresAt.UTC().Format(time.RFC3339Nano),
		Hit:              result.CacheHit,
		RemainingSeconds: remainingCacheSeconds(s.currentTime().UTC(), result.cacheExpiresAt),
		Status:           status,
		TTLSeconds:       result.cacheTTLSeconds,
	})
	if err != nil {
		return
	}
	// Append at end to preserve upstream field order.
	body := jsonappend.AppendTailFields([]byte(result.Body),
		jsonappend.Field{Key: responseCacheMetadataField, Value: json.RawMessage(metadata)},
	)
	if len(body) > maxCachedResponseBytes*2 {
		return
	}
	result.Body = string(body)
	result.ContentType = "application/json; charset=utf-8"
	if !result.ResponseNormalized {
		result.ResponseSize = int64(len(body))
	}
}

func remainingCacheSeconds(now, expiresAt time.Time) int64 {
	remaining := expiresAt.Sub(now)
	if remaining <= 0 {
		return 0
	}
	return int64((remaining + time.Second - 1) / time.Second)
}

func responseCacheReadContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(ctx, responseCacheReadTimeout)
}

func responseCacheWriteContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	// A client disconnect or an upstream timeout must not discard a response
	// that has already been obtained. Detach only the cancellation/deadline;
	// retain context values and cap the Redis operation with a short timeout.
	return context.WithTimeout(context.WithoutCancel(ctx), responseCacheWriteTimeout)
}

func responseCacheTTL(api InterfaceConfig, result OpenResult) time.Duration {
	if api.CacheEnabled != 1 || !result.Success {
		return 0
	}
	for _, rule := range api.CacheRules {
		if cacheRuleMatches(rule, result.ResponseStatus, result.Body) {
			return boundedCacheTTL(rule.TTLSeconds)
		}
	}
	return boundedCacheTTL(api.CacheDefaultTTLSeconds)
}

func boundedCacheTTL(seconds int) time.Duration {
	if seconds <= 0 {
		return 0
	}
	ttl := time.Duration(seconds) * time.Second
	if ttl > maxGatewayCacheTTL {
		return maxGatewayCacheTTL
	}
	return ttl
}

func cacheRuleMatches(rule ResponseCacheRule, status int, body string) bool {
	if rule.StatusCode != 0 && rule.StatusCode != status {
		return false
	}
	field := strings.TrimSpace(rule.FieldName)
	if field == "" {
		return rule.StatusCode != 0
	}
	actualValue, found := jsonpathvalue.Lookup(body, field)
	if strings.EqualFold(strings.TrimSpace(rule.Operator), "EXISTS") {
		return found
	}
	if !found {
		return false
	}
	actual := fmt.Sprintf("%v", actualValue)
	expected := rule.ExpectedValue
	switch strings.ToUpper(strings.TrimSpace(rule.Operator)) {
	case "EQ":
		return actual == expected
	case "NE":
		return actual != expected
	case "CONTAINS":
		return strings.Contains(actual, expected)
	case "GT", "LT":
		left, leftErr := decimalvalue.Parse(actual)
		right, rightErr := decimalvalue.Parse(expected)
		if leftErr != nil || rightErr != nil {
			return false
		}
		comparison := left.Cmp(right)
		if strings.EqualFold(rule.Operator, "GT") {
			return comparison > 0
		}
		return comparison < 0
	default:
		return false
	}
}

func responseCacheKey(api InterfaceConfig, method string, req OpenRequest) (string, error) {
	query := make(map[string][]string, len(req.Query))
	for key, values := range req.Query {
		if strings.EqualFold(key, "key") {
			continue
		}
		query[key] = append([]string(nil), values...)
	}
	headers := cacheRelevantHeaders(req.Headers)
	normalizedMethod := strings.ToUpper(strings.TrimSpace(method))
	// invokeOne supplies this default before forwarding a POST. Include the
	// effective value so callers that omit Content-Type do not create a second
	// key for the same upstream request.
	if normalizedMethod == http.MethodPost {
		if _, exists := headers["content-type"]; !exists {
			headers["content-type"] = "application/json"
		}
	}
	cacheBody := req.Body
	// Keep the key aligned with invokeOne, which sends an empty POST body as
	// an empty JSON object to the upstream.
	if normalizedMethod == http.MethodPost && len(cacheBody) == 0 {
		cacheBody = []byte("{}")
	}
	bodyHash := sha256.Sum256(cacheBody)
	effectivePollingEnabled := boolInt(pollingRuntimeEnabled(api))
	material := struct {
		APIID       int64  `json:"apiId"`
		BodyHash    string `json:"bodyHash"`
		CacheConfig any    `json:"cacheConfig"`
		ClientIP    string `json:"clientIp"`
		// DomainRouting is part of the policy fingerprint.  A route can replace
		// the effective upstream pool and response-check rules for the same
		// interface; omitting it lets a cache entry created under an older route
		// policy bypass the live polling decision.
		DomainRouting responseCacheDomainRouting `json:"domainRouting"`
		Headers       map[string]string          `json:"headers"`
		Method        string                     `json:"method"`
		Query         map[string][]string        `json:"query"`
		RequestURL    string                     `json:"requestUrl"`
		Upstreams     []responseCacheUpstream    `json:"upstreams"`
	}{
		APIID: api.ID, BodyHash: hex.EncodeToString(bodyHash[:]), ClientIP: strings.TrimSpace(req.ClientIP),
		DomainRouting: cacheRelevantDomainRouting(api.DomainRouting),
		Headers:       headers, Method: normalizedMethod, Query: query, RequestURL: strings.TrimSpace(api.RequestURL),
		Upstreams: cacheRelevantUpstreams(api),
		CacheConfig: struct {
			PolicyVersion           int64                       `json:"policyVersion"`
			Enabled                 int                         `json:"enabled"`
			DefaultTTL              int                         `json:"defaultTtl"`
			Rules                   []ResponseCacheRule         `json:"rules"`
			PollingEnabled          int                         `json:"pollingEnabled"`
			Polling                 string                      `json:"polling"`
			PollingCheckEnabled     int                         `json:"pollingCheckEnabled"`
			PollingCheckField       string                      `json:"pollingCheckField"`
			PollingCheckExpected    string                      `json:"pollingCheckExpected"`
			PollingStopCode         string                      `json:"pollingStopCode"`
			ResponseCleaningEnabled int                         `json:"responseCleaningEnabled"`
			ResponseFieldMappings   []jsoncleaning.FieldMapping `json:"responseFieldMappings,omitempty"`
		}{
			PolicyVersion:           normalizedPolicyVersion(api.CachePolicyVersion),
			Enabled:                 api.CacheEnabled,
			DefaultTTL:              api.CacheDefaultTTLSeconds,
			Rules:                   cacheRelevantRules(api.CacheRules),
			PollingEnabled:          effectivePollingEnabled,
			Polling:                 normalizedPollingMode(api.PollingMode, effectivePollingEnabled),
			PollingCheckEnabled:     api.PollingCheckEnabled,
			PollingCheckField:       defaultText(api.PollingCheckField, "code"),
			PollingCheckExpected:    defaultText(api.PollingCheckExpected, "200"),
			PollingStopCode:         strings.TrimSpace(api.PollingStopCode),
			ResponseCleaningEnabled: api.ResponseCleaningEnabled,
			ResponseFieldMappings:   cacheRelevantMappings(api.ResponseFieldMappings),
		},
	}
	data, err := json.Marshal(material)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return fmt.Sprintf("gateway:response-cache:%s:%d:%s", responseCacheKeyVersion, api.ID, hex.EncodeToString(sum[:])), nil
}

func cacheRelevantUpstreams(api InterfaceConfig) []responseCacheUpstream {
	configs := api.UpstreamConfigs
	result := make([]responseCacheUpstream, 0, len(configs))
	for _, config := range configs {
		url := strings.TrimSpace(config.URL)
		if url == "" {
			continue
		}
		result = append(result, responseCacheUpstream{
			URL:                   url,
			PollingCheckEnabled:   config.PollingCheckEnabled,
			PollingCheckExpected:  defaultText(config.PollingCheckExpected, "200"),
			PollingCheckField:     defaultText(config.PollingCheckField, "code"),
			ResponseFieldMappings: cacheRelevantMappings(config.ResponseFieldMappings),
		})
	}
	return result
}

func cacheRelevantDomainRouting(routing DomainRoutingConfig) responseCacheDomainRouting {
	result := responseCacheDomainRouting{
		Enabled:        routing.Enabled,
		Fallback:       strings.ToUpper(strings.TrimSpace(routing.Fallback)),
		DefaultRouteID: strings.TrimSpace(routing.DefaultRouteID),
	}
	if len(routing.Routes) == 0 {
		return result
	}
	result.Routes = make([]responseCacheDomainRoute, 0, len(routing.Routes))
	for _, route := range routing.Routes {
		result.Routes = append(result.Routes, responseCacheDomainRoute{
			ID:                      strings.TrimSpace(route.ID),
			Enabled:                 route.Enabled,
			Priority:                route.Priority,
			Match:                   cacheRelevantDomainMatch(route.Match),
			RequestURL:              strings.TrimSpace(route.RequestURL),
			PollingEnabled:          route.PollingEnabled,
			PollingMode:             normalizedPollingMode(route.PollingMode, route.PollingEnabled),
			PollingCheckEnabled:     route.PollingCheckEnabled,
			PollingCheckExpected:    defaultText(route.PollingCheckExpected, "200"),
			PollingCheckField:       defaultText(route.PollingCheckField, "code"),
			PollingStopCode:         strings.TrimSpace(route.PollingStopCode),
			ResponseCleaningEnabled: route.ResponseCleaningEnabled,
			ResponseFieldMappings:   cacheRelevantMappings(route.ResponseFieldMappings),
			UpstreamConfigs:         cacheRelevantDomainUpstreams(route.UpstreamConfigs),
		})
	}
	return result
}

func cacheRelevantDomainMatch(match domainrouting.MatchConfig) domainrouting.MatchConfig {
	return domainrouting.MatchConfig{
		Source:    strings.ToUpper(strings.TrimSpace(match.Source)),
		Field:     strings.TrimSpace(match.Field),
		Mode:      strings.ToUpper(strings.TrimSpace(match.Mode)),
		ValueMode: strings.ToUpper(strings.TrimSpace(match.ValueMode)),
		Domains:   append([]string(nil), match.Domains...),
	}
}

func cacheRelevantDomainUpstreams(configs []UpstreamConfig) []responseCacheUpstream {
	if len(configs) == 0 {
		return nil
	}
	result := make([]responseCacheUpstream, 0, len(configs))
	for _, config := range configs {
		url := strings.TrimSpace(config.URL)
		if url == "" {
			continue
		}
		result = append(result, responseCacheUpstream{
			URL:                   url,
			PollingCheckEnabled:   config.PollingCheckEnabled,
			PollingCheckExpected:  defaultText(config.PollingCheckExpected, "200"),
			PollingCheckField:     defaultText(config.PollingCheckField, "code"),
			ResponseFieldMappings: cacheRelevantMappings(config.ResponseFieldMappings),
		})
	}
	return result
}

func cacheRelevantMappings(mappings []jsoncleaning.FieldMapping) []jsoncleaning.FieldMapping {
	if len(mappings) == 0 {
		return nil
	}
	result := make([]jsoncleaning.FieldMapping, 0, len(mappings))
	for _, mapping := range mappings {
		source := strings.TrimSpace(mapping.Source)
		target := strings.TrimSpace(mapping.Target)
		if source == "" || target == "" {
			continue
		}
		result = append(result, jsoncleaning.FieldMapping{Source: source, Target: target})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Source == result[j].Source {
			return result[i].Target < result[j].Target
		}
		return result[i].Source < result[j].Source
	})
	return result
}

func cacheRelevantRules(rules []ResponseCacheRule) []ResponseCacheRule {
	if len(rules) == 0 {
		return nil
	}
	result := make([]ResponseCacheRule, 0, len(rules))
	for _, rule := range rules {
		result = append(result, ResponseCacheRule{
			ExpectedValue: rule.ExpectedValue,
			FieldName:     strings.TrimSpace(rule.FieldName),
			Name:          strings.TrimSpace(rule.Name),
			Operator:      strings.ToUpper(strings.TrimSpace(rule.Operator)),
			SortNo:        rule.SortNo,
			StatusCode:    rule.StatusCode,
			TTLSeconds:    rule.TTLSeconds,
		})
	}
	return result
}

func cacheRelevantHeaders(headers map[string]string) map[string]string {
	values := make(map[string]string, len(headers))
	for name, value := range headers {
		key := strings.ToLower(strings.TrimSpace(name))
		value = strings.TrimSpace(value)
		if key == "" || value == "" || strings.ContainsAny(value, "\r\n") || responseCacheHeaderIgnored(key) {
			continue
		}
		values[key] = value
	}
	return values
}

// responseCacheHeaderIgnored filters request metadata that is commonly
// regenerated by browsers and proxies. Keeping those headers in the key would
// turn every otherwise identical request into a Redis miss. Representation and
// stable business headers are intentionally retained.
func responseCacheHeaderIgnored(name string) bool {
	switch name {
	case "authorization", "cookie", "x-api-key", "host", "content-length", "proxy",
		"connection", "proxy-authorization", "proxy-authenticate", "proxy-connection",
		"transfer-encoding", "te", "trailer", "upgrade", "keep-alive", "via",
		"forwarded", "true-client-ip", "user-agent", "origin", "referer",
		"accept-encoding", "cache-control", "pragma", "range", "if-match",
		"if-none-match", "if-modified-since", "if-unmodified-since", "if-range",
		"expect", "max-forwards", "dnt", "priority", "purpose", "save-data",
		"early-data", "device-memory", "downlink", "ect", "rtt", "viewport-width", "width",
		"x-forwarded-for", "x-forwarded-host", "x-forwarded-port", "x-forwarded-proto",
		"x-forwarded-server", "x-real-ip", "x-client-ip", "x-cluster-client-ip",
		"cf-connecting-ip", "cf-ray", "cf-visitor", "x-request-id", "traceparent",
		"tracestate", "baggage", "sentry-trace":
		return true
	}
	for _, prefix := range responseCacheIgnoredHeaderPrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

func (s *Service) applyResponseAdvertisement(ctx context.Context, api InterfaceConfig, result *OpenResult) {
	if result == nil || !result.Success || api.ResponseAdEnabled != 1 || s.responseAdProvider == nil {
		return
	}
	field := strings.TrimSpace(api.ResponseAdField)
	if field == "" {
		field = "_advertisement"
	}
	// Quick check if field already exists to avoid unnecessary ad resolution.
	if strings.Contains(result.Body, `"`+field+`"`) {
		// Defensive: only a top-level key matters; full parse is expensive.
		// If a nested value happens to contain this string we accept the
		// false-positive and skip injection rather than re-marshaling.
		return
	}
	advertisement, found, err := s.responseAdProvider.ResolveResponseAd(ctx, api.ResponseAdID)
	if err != nil {
		s.warnDeliveryPolicy("resolve response advertisement", api.ID, err)
		return
	}
	if !found {
		return
	}
	payload := map[string]any{
		"id": advertisement.ID, "name": advertisement.Title, "title": advertisement.Title,
		"description": advertisement.Description, "imageUrl": advertisement.ImageURL,
		"linkUrl": advertisement.LinkURL, "url": advertisement.LinkURL, "linkTarget": advertisement.LinkTarget,
	}
	if len(advertisement.Payload) > 0 {
		payload["payload"] = advertisement.Payload
	}
	// Append at end to preserve upstream field order.
	body := jsonappend.AppendTailFields([]byte(result.Body),
		jsonappend.Field{Key: field, Value: payload},
	)
	if len(body) > maxCachedResponseBytes*2 {
		return
	}
	result.Body = string(body)
	result.ContentType = "application/json; charset=utf-8"
	if !result.ResponseNormalized {
		result.ResponseSize = int64(len(body))
	}
}

func (s *Service) warnDeliveryPolicy(operation string, apiID int64, err error) {
	if s.log != nil && err != nil {
		s.log.Warn("gateway delivery policy degraded", zap.String("operation", operation), zap.Int64("interface_id", apiID), zap.String("error", masking.Error(err)))
	}
}
