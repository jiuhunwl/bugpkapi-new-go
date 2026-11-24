package gateway

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
)

// interfaceConfigTTL bounds how long a resolved interface configuration is
// reused before the next request refreshes it from MySQL. Interface settings
// (price, points, upstreams, validation, delivery policy, status, billing
// rules) change only through administrator edits, so a short TTL keeps the
// gateway hot path at a single in-process map read while still propagating a
// config change within a few seconds. If an immediate, edit-time propagation
// is ever required, add an explicit invalidation hook here and call it from the
// interface administration handlers.
const interfaceConfigTTL = 10 * time.Second

// interfaceConfigCache is a small, mutex-guarded TTL cache for InterfaceConfig
// values keyed by api_code. It stores the configuration exactly as returned by
// the repository — including the still-encrypted proxy-pool password — so a
// cache hit never leaks a decrypted secret into memory longer than necessary.
type interfaceConfigCache struct {
	mu      sync.RWMutex
	ttl     time.Duration
	clock   func() time.Time
	items   map[string]interfaceCacheEntry
	loading map[string]*interfaceConfigLoad
	// generations invalidates in-flight loads as well as completed entries.
	// Without a generation token, an administrator update racing a cache miss
	// could let the old database snapshot repopulate the cache after
	// invalidate() returned, making the new polling policy appear to apply only
	// intermittently until the TTL elapsed.
	generations map[string]uint64
}

type interfaceCacheEntry struct {
	config    InterfaceConfig
	expiresAt time.Time
}

type interfaceConfigLoad struct {
	done       chan struct{}
	config     InterfaceConfig
	err        error
	generation uint64
}

var errInterfaceConfigLoaderPanic = errors.New("interface config loader panicked")

func newInterfaceConfigCache(ttl time.Duration) *interfaceConfigCache {
	if ttl <= 0 {
		ttl = interfaceConfigTTL
	}
	return &interfaceConfigCache{
		ttl:         ttl,
		clock:       time.Now,
		items:       make(map[string]interfaceCacheEntry),
		loading:     make(map[string]*interfaceConfigLoad),
		generations: make(map[string]uint64),
	}
}

func normalizeInterfaceConfigCacheKey(apiCode string) string {
	return strings.ToLower(strings.TrimSpace(apiCode))
}

func (c *interfaceConfigCache) get(now time.Time, apiCode string) (InterfaceConfig, bool) {
	if c == nil {
		return InterfaceConfig{}, false
	}
	apiCode = normalizeInterfaceConfigCacheKey(apiCode)
	c.mu.RLock()
	defer c.mu.RUnlock()
	entry, ok := c.items[apiCode]
	if !ok || now.After(entry.expiresAt) {
		return InterfaceConfig{}, false
	}
	return entry.config, true
}

func (c *interfaceConfigCache) set(now time.Time, apiCode string, config InterfaceConfig) {
	if c == nil {
		return
	}
	apiCode = normalizeInterfaceConfigCacheKey(apiCode)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items[apiCode] = interfaceCacheEntry{config: config, expiresAt: now.Add(c.ttl)}
}

// invalidate removes cached interface policies immediately after an
// administrator changes an interface. Codes are normalized at the boundary so
// callers can safely pass values originating from form/database input.
func (c *interfaceConfigCache) invalidate(apiCodes ...string) {
	if c == nil || len(apiCodes) == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, apiCode := range apiCodes {
		apiCode = normalizeInterfaceConfigCacheKey(apiCode)
		if apiCode == "*" {
			c.items = make(map[string]interfaceCacheEntry)
			// Invalidate every generation so in-flight loads cannot republish a
			// snapshot that predates the global policy update.
			for code := range c.generations {
				c.generations[code]++
			}
			for code := range c.loading {
				c.generations[code]++
			}
			continue
		}
		if apiCode != "" {
			delete(c.items, apiCode)
			// Bump the generation even when no completed entry exists. A load
			// already in progress may otherwise write its stale snapshot after
			// this invalidation call.
			c.generations[apiCode]++
		}
	}
}

// getOrLoad coalesces concurrent misses for the same API code. A short TTL is
// useful for propagating administrator edits, but without this guard an expiry
// boundary can turn one cache miss into a database connection burst.
func (c *interfaceConfigCache) getOrLoad(
	ctx context.Context,
	now time.Time,
	apiCode string,
	loader func() (InterfaceConfig, error),
) (InterfaceConfig, error) {
	if c == nil {
		return loader()
	}
	apiCode = normalizeInterfaceConfigCacheKey(apiCode)
	for {
		c.mu.Lock()
		if entry, ok := c.items[apiCode]; ok && !now.After(entry.expiresAt) {
			c.mu.Unlock()
			return entry.config, nil
		}
		generation := c.generations[apiCode]
		if call, ok := c.loading[apiCode]; ok && call.generation == generation {
			c.mu.Unlock()
			select {
			case <-call.done:
				// An invalidation may have happened while this caller waited.
				// Never return that stale snapshot; join or start the current
				// generation instead so a just-saved polling policy is visible to
				// every request, not only requests arriving after the TTL.
				c.mu.RLock()
				stale := c.generations[apiCode] != call.generation
				c.mu.RUnlock()
				if stale {
					continue
				}
				return call.config, call.err
			case <-ctx.Done():
				return InterfaceConfig{}, ctx.Err()
			}
		}
		call := &interfaceConfigLoad{done: make(chan struct{}), generation: generation}
		c.loading[apiCode] = call
		c.mu.Unlock()

		var panicValue any
		func() {
			defer func() { panicValue = recover() }()
			call.config, call.err = loader()
		}()
		if panicValue != nil {
			call.config = InterfaceConfig{}
			call.err = errInterfaceConfigLoaderPanic
		}
		c.mu.Lock()
		// Publish a loaded value only when no invalidation happened while the
		// loader was running. The same lock makes the generation check and cache
		// write atomic with invalidate().
		stale := c.generations[apiCode] != call.generation
		if call.err == nil && !stale {
			loadedAt := c.clock()
			c.items[apiCode] = interfaceCacheEntry{config: call.config, expiresAt: loadedAt.Add(c.ttl)}
		}
		// An invalidation may have allowed a newer generation to start loading
		// before this older call completed. Do not delete or close the newer call's
		// state; each load owns and closes its own completion channel.
		if current, ok := c.loading[apiCode]; ok && current == call {
			delete(c.loading, apiCode)
		}
		close(call.done)
		c.mu.Unlock()
		if panicValue != nil {
			panic(panicValue)
		}
		if stale {
			// The caller that performed the stale load also retries instead of
			// exposing one request to the pre-invalidation policy.
			continue
		}
		return call.config, call.err
	}
}

// interfaceByCode resolves an interface configuration, serving cache hits
// without touching MySQL. The returned value is a copy, so the caller may
// decrypt the proxy-pool password in place without mutating the cached entry.
func (s *Service) interfaceByCode(ctx context.Context, apiCode string) (InterfaceConfig, error) {
	return s.interfaceCache.getOrLoad(ctx, s.now(), apiCode, func() (InterfaceConfig, error) {
		return s.repo.InterfaceByCode(ctx, apiCode)
	})
}
