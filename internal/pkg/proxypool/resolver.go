package proxypool

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"bugpk-api/internal/pkg/httpclient"
)

const (
	maxPoolResponseBytes    = int64(256 << 10)
	maxResolverCacheEntries = 256
	maxResolverGrace        = 5 * time.Minute
	minResolverGrace        = 30 * time.Second
)

type Resolver struct {
	client           *http.Client
	mu               sync.Mutex
	cache            map[string]cacheEntry
	refreshes        map[string]*refreshCall
	generations      map[string]uint64
	managedClients   map[managedClientCacheKey]managedClientCacheEntry
	managedClientUse uint64
	now              func() time.Time
}

type cacheEntry struct {
	endpoint   Endpoint
	expires    time.Time
	graceUntil time.Time
}

type refreshCall struct {
	done       chan struct{}
	generation uint64
	endpoint   Endpoint
	err        error
}

type managedClientCacheKey struct {
	base   *http.Client
	config string
}

type managedClientCacheEntry struct {
	client   *http.Client
	lastUsed uint64
	proxyURL string
}

func NewResolver(client *http.Client) *Resolver {
	if client == nil {
		client = httpclient.Outbound(5 * time.Second)
	}
	return &Resolver{
		client:         client,
		cache:          make(map[string]cacheEntry),
		refreshes:      make(map[string]*refreshCall),
		generations:    make(map[string]uint64),
		managedClients: make(map[managedClientCacheKey]managedClientCacheEntry),
		now:            time.Now,
	}
}

func (r *Resolver) Client(ctx context.Context, base *http.Client, cfg Config) (*http.Client, error) {
	if cfg.Enabled != 1 {
		return base, nil
	}
	endpoint, err := r.Resolve(ctx, cfg)
	if err != nil {
		return nil, err
	}
	normalized, err := Normalize(cfg)
	if err != nil {
		return nil, err
	}
	key := managedClientCacheKey{base: base, config: configCacheKey(normalized)}
	proxyURL := endpoint.ProxyURL()

	r.mu.Lock()
	r.managedClientUse++
	lastUsed := r.managedClientUse
	if entry, ok := r.managedClients[key]; ok && entry.proxyURL == proxyURL {
		entry.lastUsed = lastUsed
		r.managedClients[key] = entry
		r.mu.Unlock()
		return entry.client, nil
	}
	client, err := httpclient.WithProxyURL(base, proxyURL)
	if err != nil {
		r.mu.Unlock()
		return nil, err
	}
	var staleClients []*http.Client
	if entry, ok := r.managedClients[key]; ok {
		staleClients = append(staleClients, entry.client)
	} else {
		for len(r.managedClients) >= maxResolverCacheEntries {
			var leastRecentlyUsedKey managedClientCacheKey
			var leastRecentlyUsed uint64
			found := false
			for cacheKey, entry := range r.managedClients {
				if !found || entry.lastUsed < leastRecentlyUsed {
					leastRecentlyUsedKey = cacheKey
					leastRecentlyUsed = entry.lastUsed
					found = true
				}
			}
			if !found {
				break
			}
			staleClients = append(staleClients, r.managedClients[leastRecentlyUsedKey].client)
			delete(r.managedClients, leastRecentlyUsedKey)
		}
	}
	r.managedClients[key] = managedClientCacheEntry{client: client, lastUsed: lastUsed, proxyURL: proxyURL}
	r.mu.Unlock()
	for _, staleClient := range staleClients {
		staleClient.CloseIdleConnections()
	}
	return client, nil
}

// ClientFresh invalidates the currently selected API-pool endpoint and obtains
// a new managed client. It is intentionally separate from Client so callers
// can perform at most one forced refresh for a single business request.
func (r *Resolver) ClientFresh(ctx context.Context, base *http.Client, cfg Config) (*http.Client, error) {
	if cfg.Enabled != 1 {
		return base, nil
	}
	r.Invalidate(cfg)
	return r.Client(ctx, base, cfg)
}

// Invalidate removes the cached endpoint and managed transport for a proxy
// configuration. A generation counter prevents an in-flight refresh started
// before invalidation from repopulating the cache with the old endpoint.
func (r *Resolver) Invalidate(cfg Config) {
	normalized, err := Normalize(cfg)
	if err != nil || normalized.Source != SourceAPI {
		return
	}
	key := configCacheKey(normalized)
	r.mu.Lock()
	r.generations[key]++
	delete(r.cache, key)
	var staleClients []*http.Client
	for managedKey, entry := range r.managedClients {
		if managedKey.config != key {
			continue
		}
		staleClients = append(staleClients, entry.client)
		delete(r.managedClients, managedKey)
	}
	r.mu.Unlock()
	for _, client := range staleClients {
		client.CloseIdleConnections()
	}
}

var errRefreshInvalidated = errors.New("代理池刷新已失效")

func (r *Resolver) Resolve(ctx context.Context, cfg Config) (Endpoint, error) {
	normalized, err := Normalize(cfg)
	if err != nil {
		return Endpoint{}, err
	}
	if normalized.Enabled != 1 {
		return Endpoint{}, errors.New("代理池未启用")
	}
	if normalized.Source == SourceFixed {
		return FixedEndpoint(normalized)
	}
	key := configCacheKey(normalized)
	for {
		now := r.now()
		r.mu.Lock()
		entry, ok := r.cache[key]
		if ok && now.Before(entry.expires) {
			r.mu.Unlock()
			return entry.endpoint, nil
		}
		if call, exists := r.refreshes[key]; exists {
			done := call.done
			generation := call.generation
			r.mu.Unlock()
			select {
			case <-done:
			case <-ctx.Done():
				return Endpoint{}, ctx.Err()
			}
			r.mu.Lock()
			invalidated := r.generations[key] != generation
			endpoint, refreshErr := call.endpoint, call.err
			r.mu.Unlock()
			if invalidated || errors.Is(refreshErr, errRefreshInvalidated) {
				continue
			}
			return endpoint, refreshErr
		}
		generation := r.generations[key]
		call := &refreshCall{done: make(chan struct{}), generation: generation}
		previous := entry
		r.refreshes[key] = call
		r.mu.Unlock()

		endpoint, _, fetchErr := r.fetch(ctx, normalized)
		r.mu.Lock()
		now = r.now()
		currentGeneration := r.generations[key]
		if currentGeneration != generation {
			call.err = errRefreshInvalidated
		} else if fetchErr == nil {
			for cacheKey, cached := range r.cache {
				removeAfter := cached.graceUntil
				if removeAfter.IsZero() {
					removeAfter = cached.expires
				}
				if !now.Before(removeAfter) {
					delete(r.cache, cacheKey)
				}
			}
			if _, exists := r.cache[key]; !exists {
				for len(r.cache) >= maxResolverCacheEntries {
					var earliestKey string
					var earliestExpiry time.Time
					for cacheKey, cached := range r.cache {
						if earliestKey == "" || cached.expires.Before(earliestExpiry) {
							earliestKey = cacheKey
							earliestExpiry = cached.expires
						}
					}
					if earliestKey == "" {
						break
					}
					delete(r.cache, earliestKey)
				}
			}
			expires := now.Add(time.Duration(normalized.RefreshSecs) * time.Second)
			grace := resolverGrace(normalized.RefreshSecs)
			r.cache[key] = cacheEntry{endpoint: endpoint, expires: expires, graceUntil: expires.Add(grace)}
			call.endpoint = endpoint
		} else if previous.endpoint.Host != "" && now.Before(previous.graceUntil) {
			call.endpoint = previous.endpoint
			call.err = nil
		} else {
			call.err = fetchErr
			delete(r.cache, key)
		}
		if call.err == nil && call.endpoint.Host == "" {
			call.err = fetchErr
		}
		if r.refreshes[key] == call {
			delete(r.refreshes, key)
		}
		close(call.done)
		resultEndpoint, resultErr := call.endpoint, call.err
		invalidated := currentGeneration != generation
		r.mu.Unlock()
		if invalidated || errors.Is(resultErr, errRefreshInvalidated) {
			continue
		}
		return resultEndpoint, resultErr
	}
}

func resolverGrace(refreshSeconds int) time.Duration {
	grace := time.Duration(refreshSeconds*2) * time.Second
	if grace < minResolverGrace {
		grace = minResolverGrace
	}
	if grace > maxResolverGrace {
		grace = maxResolverGrace
	}
	return grace
}

func (r *Resolver) Inspect(ctx context.Context, cfg Config) (Endpoint, []Candidate, error) {
	normalized, err := Normalize(cfg)
	if err != nil {
		return Endpoint{}, nil, err
	}
	if normalized.Enabled != 1 || normalized.Source != SourceAPI {
		return Endpoint{}, nil, errors.New("只有代理池接口配置可以识别返回标签")
	}
	return r.fetch(ctx, normalized)
}

func (r *Resolver) fetch(ctx context.Context, cfg Config) (Endpoint, []Candidate, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.APIURL, nil)
	if err != nil {
		return Endpoint{}, nil, err
	}
	request.Header.Set("Accept", "application/json")
	for _, item := range cfg.APIHeaders {
		request.Header.Set(item.Name, item.Value)
	}
	response, err := r.client.Do(request)
	if err != nil {
		return Endpoint{}, nil, fmt.Errorf("请求代理池接口失败: %w", err)
	}
	defer response.Body.Close()
	data, err := httpclient.ReadBody(response.Body, maxPoolResponseBytes)
	if err != nil {
		return Endpoint{}, nil, fmt.Errorf("读取代理池接口响应失败: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return Endpoint{}, nil, fmt.Errorf("代理池接口返回 HTTP %d", response.StatusCode)
	}
	return ParseAPIResponse(cfg, data)
}

func (endpoint Endpoint) ProxyURL() string {
	value := &url.URL{
		Scheme: strings.ToLower(endpoint.Protocol),
		Host:   net.JoinHostPort(endpoint.Host, strconv.Itoa(endpoint.Port)),
	}
	if endpoint.Username != "" || endpoint.Password != "" {
		value.User = url.UserPassword(endpoint.Username, endpoint.Password)
	}
	return value.String()
}

func configCacheKey(cfg Config) string {
	data, _ := json.Marshal(cfg)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
