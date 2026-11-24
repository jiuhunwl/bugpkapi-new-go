package proxypool

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"bugpk-api/internal/pkg/httpclient"
)

func TestResolverFetchesAuthenticatedPoolAndCachesEndpoint(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		if request.Header.Get("X-Pool-Token") != "secret-token" {
			t.Fatalf("X-Pool-Token = %q", request.Header.Get("X-Pool-Token"))
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"data":{"proxy":"203.0.113.30:3128"}}`))
	}))
	defer server.Close()

	resolver := NewResolver(httpclient.New(httpclient.Options{AllowPrivateNetworks: true, Timeout: time.Second}))
	cfg := Config{
		APIHeaders:  []Header{{Name: "X-Pool-Token", Value: "secret-token"}},
		APIURL:      server.URL,
		Enabled:     1,
		Protocol:    ProtocolHTTP,
		RefreshSecs: 60,
		Source:      SourceAPI,
	}
	for range 2 {
		endpoint, err := resolver.Resolve(context.Background(), cfg)
		if err != nil {
			t.Fatalf("Resolve() error = %v", err)
		}
		if endpoint.Host != "203.0.113.30" || endpoint.Port != 3128 {
			t.Fatalf("endpoint = %+v", endpoint)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("pool calls = %d, want 1", calls.Load())
	}
}

func TestResolverClientReusesManagedClientForSameProxyConfig(t *testing.T) {
	base := httpclient.New(httpclient.Options{AllowPrivateNetworks: true, Timeout: time.Second})
	resolver := NewResolver(base)
	cfg := fixedProxyConfig("proxy.example.com", 8080)

	first, err := resolver.Client(context.Background(), base, cfg)
	if err != nil {
		t.Fatalf("Client(first) error = %v", err)
	}
	second, err := resolver.Client(context.Background(), base, cfg)
	if err != nil {
		t.Fatalf("Client(second) error = %v", err)
	}
	if second != first {
		t.Fatal("same base and proxy config returned different managed clients")
	}
}

func TestResolverClientConcurrentSameConfigReturnsSingleClient(t *testing.T) {
	base := httpclient.New(httpclient.Options{AllowPrivateNetworks: true, Timeout: time.Second})
	resolver := NewResolver(base)
	cfg := fixedProxyConfig("proxy.example.com", 8080)
	const callers = 32
	results := make(chan *http.Client, callers)
	errors := make(chan error, callers)
	var start sync.WaitGroup
	start.Add(1)
	for range callers {
		go func() {
			start.Wait()
			client, err := resolver.Client(context.Background(), base, cfg)
			results <- client
			errors <- err
		}()
	}
	start.Done()

	var first *http.Client
	for range callers {
		if err := <-errors; err != nil {
			t.Fatalf("Client() error = %v", err)
		}
		client := <-results
		if first == nil {
			first = client
			continue
		}
		if client != first {
			t.Fatal("concurrent same-config calls returned different managed clients")
		}
	}
}

func TestResolverClientReplacesRotatedEndpointAndClosesOldIdleConnections(t *testing.T) {
	var poolCalls atomic.Int64
	poolServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		poolCall := poolCalls.Add(1)
		host := "proxy-a.example.com"
		if poolCall > 1 {
			host = "proxy-b.example.com"
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"proxy":"` + host + `:8080"}`))
	}))
	defer poolServer.Close()

	proxyServer, proxyAddress, idle, closed := observedProxyServer(t)
	defer proxyServer.Close()
	base := observedProxyBaseClient(proxyAddress)
	resolver := NewResolver(httpclient.New(httpclient.Options{AllowPrivateNetworks: true, Timeout: time.Second}))
	now := time.Date(2026, time.August, 18, 15, 0, 0, 0, time.UTC)
	resolver.now = func() time.Time { return now }
	cfg := Config{
		APIURL:      poolServer.URL,
		Enabled:     1,
		Protocol:    ProtocolHTTP,
		RefreshSecs: 5,
		Source:      SourceAPI,
	}
	first, err := resolver.Client(context.Background(), base, cfg)
	if err != nil {
		t.Fatalf("Client(first endpoint) error = %v", err)
	}
	performObservedProxyRequest(t, first, idle)

	now = now.Add(6 * time.Second)
	second, err := resolver.Client(context.Background(), base, cfg)
	if err != nil {
		t.Fatalf("Client(rotated endpoint) error = %v", err)
	}
	if second == first {
		t.Fatal("rotated proxy endpoint reused the previous managed client")
	}
	waitForConnectionState(t, closed, "old proxy idle connection to close after endpoint rotation")
}

func TestResolverClientCacheEvictsLeastRecentlyUsedAndClosesIdleConnections(t *testing.T) {
	proxyServer, proxyAddress, idle, closed := observedProxyServer(t)
	defer proxyServer.Close()
	base := observedProxyBaseClient(proxyAddress)
	resolver := NewResolver(base)

	firstConfig := fixedProxyConfig("proxy-0.example.com", 8080)
	first, err := resolver.Client(context.Background(), base, firstConfig)
	if err != nil {
		t.Fatalf("Client(first) error = %v", err)
	}
	performObservedProxyRequest(t, first, idle)

	var secondConfig Config
	var second *http.Client
	for index := 1; index <= maxResolverCacheEntries; index++ {
		cfg := fixedProxyConfig("proxy-"+strconv.Itoa(index)+".example.com", 8080)
		client, err := resolver.Client(context.Background(), base, cfg)
		if err != nil {
			t.Fatalf("Client(config %d) error = %v", index, err)
		}
		if index == 1 {
			secondConfig = cfg
			second = client
		}
	}
	waitForConnectionState(t, closed, "least-recently-used proxy idle connection to close after eviction")

	secondAgain, err := resolver.Client(context.Background(), base, secondConfig)
	if err != nil {
		t.Fatalf("Client(second cached config) error = %v", err)
	}
	if secondAgain != second {
		t.Fatal("later proxy client was evicted instead of the least-recently-used client")
	}
	firstAgain, err := resolver.Client(context.Background(), base, firstConfig)
	if err != nil {
		t.Fatalf("Client(first evicted config) error = %v", err)
	}
	if firstAgain == first {
		t.Fatal("least-recently-used proxy client was retained beyond the cache limit")
	}
}

func TestResolverRemovesExpiredEntriesBeforeCachingNewEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"data":{"proxy":"203.0.113.31:3128"}}`))
	}))
	defer server.Close()

	resolver := NewResolver(httpclient.New(httpclient.Options{AllowPrivateNetworks: true, Timeout: time.Second}))
	now := time.Date(2026, time.August, 18, 12, 0, 0, 0, time.UTC)
	resolver.now = func() time.Time { return now }
	first := Config{
		APIURL:      server.URL + "?entry=first",
		Enabled:     1,
		Protocol:    ProtocolHTTP,
		RefreshSecs: 60,
		Source:      SourceAPI,
	}
	if _, err := resolver.Resolve(context.Background(), first); err != nil {
		t.Fatalf("Resolve(first) error = %v", err)
	}

	// A cached endpoint remains available through its stale grace period. Move
	// beyond both the 60-second TTL and its 120-second grace before asserting
	// that unrelated refresh work removes it.
	now = now.Add(181 * time.Second)
	second := first
	second.APIURL = server.URL + "?entry=second"
	if _, err := resolver.Resolve(context.Background(), second); err != nil {
		t.Fatalf("Resolve(second) error = %v", err)
	}
	if got := len(resolver.cache); got != 1 {
		t.Fatalf("cache size = %d, want 1 live entry", got)
	}
	firstNormalized, err := Normalize(first)
	if err != nil {
		t.Fatalf("Normalize(first) error = %v", err)
	}
	secondNormalized, err := Normalize(second)
	if err != nil {
		t.Fatalf("Normalize(second) error = %v", err)
	}
	if _, ok := resolver.cache[configCacheKey(firstNormalized)]; ok {
		t.Fatal("expired first cache entry was retained")
	}
	if _, ok := resolver.cache[configCacheKey(secondNormalized)]; !ok {
		t.Fatal("live second cache entry was not retained")
	}
}

func TestResolverCapsCacheAndKeepsNewestEndpoint(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"data":{"proxy":"203.0.113.32:3128"}}`))
	}))
	defer server.Close()

	resolver := NewResolver(httpclient.New(httpclient.Options{AllowPrivateNetworks: true, Timeout: time.Second}))
	now := time.Date(2026, time.August, 18, 13, 0, 0, 0, time.UTC)
	resolver.now = func() time.Time { return now }
	var earliest, next, newest Config
	for index := 0; index < 257; index++ {
		newest = Config{
			APIURL:      server.URL + "?entry=" + strconv.Itoa(index),
			Enabled:     1,
			Protocol:    ProtocolHTTP,
			RefreshSecs: index + 5,
			Source:      SourceAPI,
		}
		if index == 0 {
			earliest = newest
		}
		if index == 1 {
			next = newest
		}
		if _, err := resolver.Resolve(context.Background(), newest); err != nil {
			t.Fatalf("Resolve(entry %d) error = %v", index, err)
		}
	}
	if got := len(resolver.cache); got != 256 {
		t.Fatalf("cache size = %d, want 256", got)
	}
	earliestNormalized, err := Normalize(earliest)
	if err != nil {
		t.Fatalf("Normalize(earliest) error = %v", err)
	}
	nextNormalized, err := Normalize(next)
	if err != nil {
		t.Fatalf("Normalize(next) error = %v", err)
	}
	if _, ok := resolver.cache[configCacheKey(earliestNormalized)]; ok {
		t.Fatal("earliest-expiring cache entry was retained")
	}
	if _, ok := resolver.cache[configCacheKey(nextNormalized)]; !ok {
		t.Fatal("later-expiring cache entry was unexpectedly evicted")
	}

	callsBeforeCacheHit := calls.Load()
	if _, err := resolver.Resolve(context.Background(), newest); err != nil {
		t.Fatalf("Resolve(newest cached entry) error = %v", err)
	}
	if got := calls.Load(); got != callsBeforeCacheHit {
		t.Fatalf("pool calls after resolving newest entry = %d, want %d", got, callsBeforeCacheHit)
	}
}

func TestResolverConcurrentRefreshUsesSinglePoolRequestAtCapacity(t *testing.T) {
	arrived := make(chan struct{})
	secondRequest := make(chan struct{})
	release := make(chan struct{})
	var poolCalls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		if poolCalls.Add(1) == 1 {
			close(arrived)
		} else {
			select {
			case <-secondRequest:
			default:
				close(secondRequest)
			}
		}
		<-release
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"data":{"proxy":"203.0.113.33:3128"}}`))
	}))
	defer server.Close()

	resolver := NewResolver(httpclient.New(httpclient.Options{AllowPrivateNetworks: true, Timeout: 5 * time.Second}))
	now := time.Date(2026, time.August, 18, 14, 0, 0, 0, time.UTC)
	resolver.now = func() time.Time { return now }
	for index := 0; index < 256; index++ {
		resolver.cache["existing-"+strconv.Itoa(index)] = cacheEntry{
			endpoint: Endpoint{Protocol: ProtocolHTTP, Host: "203.0.113.40", Port: 3128},
			expires:  now.Add(time.Minute),
		}
	}
	cfg := Config{
		APIURL:      server.URL,
		Enabled:     1,
		Protocol:    ProtocolHTTP,
		RefreshSecs: 3600,
		Source:      SourceAPI,
	}
	const callers = 32
	results := make(chan error, callers)
	for range callers {
		go func() {
			_, err := resolver.Resolve(context.Background(), cfg)
			results <- err
		}()
	}
	select {
	case <-arrived:
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("timed out waiting for the pool request")
	}
	select {
	case <-secondRequest:
		close(release)
		t.Fatal("concurrent refresh issued more than one proxy-pool request")
	case <-time.After(150 * time.Millisecond):
	}
	close(release)
	for range callers {
		if err := <-results; err != nil {
			t.Fatalf("Resolve() error = %v", err)
		}
	}
	if calls := poolCalls.Load(); calls != 1 {
		t.Fatalf("pool calls = %d, want 1", calls)
	}

	if got := len(resolver.cache); got != 256 {
		t.Fatalf("cache size after same-key replacement = %d, want 256", got)
	}
	normalized, err := Normalize(cfg)
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	if _, ok := resolver.cache[configCacheKey(normalized)]; !ok {
		t.Fatal("new cache key was not retained")
	}
}

func TestResolverUsesStaleEndpointWhilePoolRefreshFailsInsideGrace(t *testing.T) {
	var failRefresh atomic.Bool
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		if failRefresh.Load() {
			http.Error(writer, "pool temporarily unavailable", http.StatusServiceUnavailable)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"proxy":"203.0.113.71:3128"}`))
	}))
	defer server.Close()

	now := time.Date(2026, time.August, 19, 12, 0, 0, 0, time.UTC)
	resolver := NewResolver(httpclient.New(httpclient.Options{AllowPrivateNetworks: true, Timeout: time.Second}))
	resolver.now = func() time.Time { return now }
	cfg := Config{
		APIURL:      server.URL,
		Enabled:     1,
		Protocol:    ProtocolHTTP,
		RefreshSecs: 5,
		Source:      SourceAPI,
	}
	first, err := resolver.Resolve(context.Background(), cfg)
	if err != nil {
		t.Fatalf("initial Resolve() error = %v", err)
	}

	failRefresh.Store(true)
	now = now.Add(6 * time.Second)
	stale, err := resolver.Resolve(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Resolve() during refresh grace error = %v", err)
	}
	if stale != first {
		t.Fatalf("stale endpoint = %+v, want last successful endpoint %+v", stale, first)
	}
	if calls.Load() != 2 {
		t.Fatalf("pool calls inside grace = %d, want 2", calls.Load())
	}

	now = now.Add(30 * time.Second)
	if _, err := resolver.Resolve(context.Background(), cfg); err == nil {
		t.Fatal("Resolve() returned a stale endpoint after the grace period")
	}
}

func TestResolverInvalidationForcesPoolRefresh(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		call := calls.Add(1)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"proxy":"203.0.113.` + strconv.FormatInt(call, 10) + `:3128"}`))
	}))
	defer server.Close()

	resolver := NewResolver(httpclient.New(httpclient.Options{AllowPrivateNetworks: true, Timeout: time.Second}))
	cfg := Config{
		APIURL:      server.URL,
		Enabled:     1,
		Protocol:    ProtocolHTTP,
		RefreshSecs: 60,
		Source:      SourceAPI,
	}
	first, err := resolver.Resolve(context.Background(), cfg)
	if err != nil {
		t.Fatalf("initial Resolve() error = %v", err)
	}
	resolver.Invalidate(cfg)
	second, err := resolver.Resolve(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Resolve() after invalidation error = %v", err)
	}
	if first == second {
		t.Fatalf("invalidation reused endpoint %+v", second)
	}
	if calls.Load() != 2 {
		t.Fatalf("pool calls after invalidation = %d, want 2", calls.Load())
	}
}

func TestEndpointProxyURLIncludesCredentials(t *testing.T) {
	endpoint := Endpoint{Protocol: ProtocolSOCKS5, Host: "2001:db8::10", Port: 1080, Username: "user", Password: "p@ss"}
	if got := endpoint.ProxyURL(); got != "socks5://user:p%40ss@[2001:db8::10]:1080" {
		t.Fatalf("ProxyURL() = %q", got)
	}
}

func fixedProxyConfig(host string, port int) Config {
	return Config{
		Enabled:  1,
		Host:     host,
		Port:     port,
		Protocol: ProtocolHTTP,
		Source:   SourceFixed,
	}
}

func observedProxyServer(t *testing.T) (*httptest.Server, string, <-chan struct{}, <-chan struct{}) {
	t.Helper()
	idle := make(chan struct{})
	closed := make(chan struct{})
	var idleOnce sync.Once
	var closedOnce sync.Once
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/plain")
		_, _ = writer.Write([]byte("proxy-ok"))
	}))
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		switch state {
		case http.StateIdle:
			idleOnce.Do(func() { close(idle) })
		case http.StateClosed:
			closedOnce.Do(func() { close(closed) })
		}
	}
	server.Start()
	return server, server.Listener.Addr().String(), idle, closed
}

func observedProxyBaseClient(proxyAddress string) *http.Client {
	return httpclient.New(httpclient.Options{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, proxyAddress)
		},
		IdleConnTimeout: time.Minute,
		LookupIP: func(context.Context, string) ([]net.IP, error) {
			return []net.IP{net.ParseIP("8.8.8.8")}, nil
		},
		Timeout: 2 * time.Second,
	})
}

func performObservedProxyRequest(t *testing.T, client *http.Client, idle <-chan struct{}) {
	t.Helper()
	response, err := client.Get("http://upstream.example.com/resource")
	if err != nil {
		t.Fatalf("proxied request error = %v", err)
	}
	if _, err := io.Copy(io.Discard, response.Body); err != nil {
		_ = response.Body.Close()
		t.Fatalf("read proxied response = %v", err)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatalf("close proxied response = %v", err)
	}
	waitForConnectionState(t, idle, "proxy connection to become idle")
}

func waitForConnectionState(t *testing.T, state <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-state:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", description)
	}
}
