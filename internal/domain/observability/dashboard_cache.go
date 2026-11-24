package observability

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

const dashboardReadCacheTTL = 2 * time.Second

type dashboardReadCache struct {
	mu      sync.Mutex
	ttl     time.Duration
	items   map[string]dashboardReadCacheEntry
	loading map[string]*dashboardReadCacheLoad
}

type dashboardReadCacheEntry struct {
	value     any
	expiresAt time.Time
}

type dashboardReadCacheLoad struct {
	done  chan struct{}
	value any
	err   error
}

var errDashboardReadCacheLoaderPanic = errors.New("dashboard cache loader panicked")

func newDashboardReadCache(ttl time.Duration) *dashboardReadCache {
	if ttl <= 0 {
		ttl = dashboardReadCacheTTL
	}
	return &dashboardReadCache{
		ttl:     ttl,
		items:   make(map[string]dashboardReadCacheEntry),
		loading: make(map[string]*dashboardReadCacheLoad),
	}
}

func (c *dashboardReadCache) getOrLoad(
	ctx context.Context,
	now time.Time,
	key string,
	loader func() (any, error),
) (any, error) {
	if c == nil {
		return loader()
	}
	c.mu.Lock()
	if entry, ok := c.items[key]; ok && !now.After(entry.expiresAt) {
		c.mu.Unlock()
		return entry.value, nil
	}
	if call, ok := c.loading[key]; ok {
		c.mu.Unlock()
		select {
		case <-call.done:
			return call.value, call.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	call := &dashboardReadCacheLoad{done: make(chan struct{})}
	c.loading[key] = call
	c.mu.Unlock()

	var panicValue any
	func() {
		defer func() { panicValue = recover() }()
		call.value, call.err = loader()
	}()
	if panicValue != nil {
		call.value = nil
		call.err = errDashboardReadCacheLoaderPanic
	}
	c.mu.Lock()
	if call.err == nil {
		c.items[key] = dashboardReadCacheEntry{
			value:     call.value,
			expiresAt: now.Add(c.ttl),
		}
	}
	if c.loading[key] == call {
		delete(c.loading, key)
	}
	close(call.done)
	c.mu.Unlock()
	if panicValue != nil {
		panic(panicValue)
	}
	return call.value, call.err
}

func dashboardCached[T any](
	cache *dashboardReadCache,
	ctx context.Context,
	key string,
	loader func() (T, error),
) (T, error) {
	var zero T
	value, err := cache.getOrLoad(ctx, time.Now(), key, func() (any, error) {
		return loader()
	})
	if err != nil {
		return zero, err
	}
	typed, ok := value.(T)
	if !ok {
		return zero, fmt.Errorf("dashboard cache type mismatch for %s", key)
	}
	return typed, nil
}
