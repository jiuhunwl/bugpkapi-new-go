package gateway

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"bugpk-api/internal/pkg/domainrouting"
	"github.com/redis/go-redis/v9"
)

// Redis is the storage boundary only; selection, cursor keys, cache policy and
// serialization all run through the production gateway.
type domainPoolRedisHook struct {
	equalHealthRoundRobinHook
	counters map[string]int64
	cache    map[string]string
}

func (h *domainPoolRedisHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		h.mu.Lock()
		defer h.mu.Unlock()
		switch strings.ToLower(cmd.Name()) {
		case "eval", "evalsha":
			key := fmt.Sprint(cmd.Args()[3])
			h.counters[key]++
			cmd.(*redis.Cmd).SetVal(h.counters[key])
			return nil
		case "set":
			value := cmd.Args()[2]
			if body, ok := value.([]byte); ok {
				h.cache[fmt.Sprint(cmd.Args()[1])] = string(body)
			} else {
				h.cache[fmt.Sprint(cmd.Args()[1])] = fmt.Sprint(value)
			}
			cmd.(*redis.StatusCmd).SetVal("OK")
			return nil
		case "get":
			value, ok := h.cache[fmt.Sprint(cmd.Args()[1])]
			if !ok {
				cmd.SetErr(redis.Nil)
				return redis.Nil
			}
			cmd.(*redis.StringCmd).SetVal(value)
			return nil
		default:
			return next(ctx, cmd)
		}
	}
}

func newDomainPoolRedis(t *testing.T) *redis.Client {
	t.Helper()
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})
	client.AddHook(&domainPoolRedisHook{counters: map[string]int64{}, cache: map[string]string{}})
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func independentDomainPoolConfig() InterfaceConfig {
	api := InterfaceConfig{ID: 9801, RequestURL: "https://default.example/api", CacheEnabled: 1, CacheDefaultTTLSeconds: 60, DomainRouting: DomainRoutingConfig{Enabled: 1, Fallback: domainrouting.FallbackReject}}
	for _, domain := range []string{"a.example", "b.example"} {
		api.DomainRouting.Routes = append(api.DomainRouting.Routes, DomainPollingRoute{
			ID: strings.ReplaceAll(domain, ".", "-"), Name: domain, Enabled: 1, Priority: 100,
			Match:      domainrouting.MatchConfig{Source: domainrouting.SourceQuery, Field: "url", Mode: domainrouting.MatchSubdomain, Domains: []string{domain}},
			RequestURL: "https://unused-primary.example/api", PollingEnabled: 1, PollingMode: "ROUND_ROBIN",
			PollingCheckEnabled: 1, PollingCheckField: "code", PollingCheckExpected: "200",
			UpstreamConfigs: []UpstreamConfig{{URL: "https://shared-one.example/api"}, {URL: "https://shared-two.example/api"}},
		})
	}
	return api
}

func TestDomainPoolsRotateIndependentlyWhenTheyShareUpstreamURLs(t *testing.T) {
	var called []string
	service := &Service{rdb: newDomainPoolRedis(t),
		httpClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			called = append(called, request.URL.Host)
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"code":200}`)), Request: request}, nil
		})},
		healthRecordScopedFn: func(context.Context, int64, string, string, bool, int64) error { return nil },
	}
	api := independentDomainPoolConfig()
	for _, domain := range []string{"a.example", "b.example", "a.example", "b.example", "a.example"} {
		result, err := service.invokeUpstreams(context.Background(), api, http.MethodGet, OpenRequest{Query: map[string][]string{"url": {"https://" + domain + "/item"}}})
		if err != nil || !result.Success {
			t.Fatalf("domain %s failed: %+v %v", domain, result, err)
		}
	}
	want := []string{"shared-one.example", "shared-one.example", "shared-two.example", "shared-two.example", "shared-one.example"}
	if !reflect.DeepEqual(called, want) {
		t.Fatalf("sub-pools shared a round-robin cursor: got %v want %v", called, want)
	}
}

func TestDomainPoolCacheRoundTripRespectsSelectedPoolAndPolicyChanges(t *testing.T) {
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	service := &Service{rdb: newDomainPoolRedis(t), now: func() time.Time { return now }}
	api := independentDomainPoolConfig()
	request := func(domain string) OpenRequest {
		return OpenRequest{Query: map[string][]string{"url": {"https://" + domain + "/item"}}}
	}
	reqA, reqB := request("a.example"), request("b.example")
	resolve := func(req OpenRequest) InterfaceConfig {
		t.Helper()
		selected, _, err := resolveDomainRoute(api, req)
		if err != nil {
			t.Fatal(err)
		}
		return selected
	}
	a, b := resolve(reqA), resolve(reqB)
	resultA := OpenResult{Body: `{"code":200,"data":"pool-a"}`, ContentType: "application/json", ResponseStatus: 200, Success: true}
	service.storeResponseCache(context.Background(), a, http.MethodGet, reqA, &resultA)
	cached, hit := service.loadResponseCache(context.Background(), a, http.MethodGet, reqA)
	if !hit || !strings.Contains(cached.Body, "pool-a") {
		t.Fatalf("selected pool did not read its cached response: hit=%v result=%+v", hit, cached)
	}
	if _, hit := service.loadResponseCache(context.Background(), b, http.MethodGet, reqB); hit {
		t.Fatal("pool-b consumed pool-a's cache")
	}
	api.DomainRouting.Routes[0].Name = "renamed"
	api.DomainRouting.Routes[0].UpstreamConfigs[0].FailureCount = 99
	if _, hit := service.loadResponseCache(context.Background(), resolve(reqA), http.MethodGet, reqA); !hit {
		t.Fatal("display name/health metadata invalidated business cache")
	}
	api.DomainRouting.Routes[0].PollingCheckExpected = "201"
	if _, hit := service.loadResponseCache(context.Background(), resolve(reqA), http.MethodGet, reqA); hit {
		t.Fatal("changed response-check policy reused the old cache")
	}
	api.DomainRouting.Routes[0].PollingCheckExpected = "200"
	api.DomainRouting.Routes[0].UpstreamConfigs[0].URL = "https://replacement.example/api"
	if _, hit := service.loadResponseCache(context.Background(), resolve(reqA), http.MethodGet, reqA); hit {
		t.Fatal("changed upstream pool reused the old cache")
	}
}
