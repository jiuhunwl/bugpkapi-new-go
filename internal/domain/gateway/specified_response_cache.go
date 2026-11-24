package gateway

import (
	"context"
	"errors"
	"sync"
	"time"
)

const specifiedResponseCacheTTL = 2 * time.Second

type specifiedResponseCache struct {
	mu      sync.Mutex
	ttl     time.Duration
	clock   func() time.Time
	items   map[int64]specifiedResponseCacheEntry
	loading map[int64]*specifiedResponseCacheLoad
	version map[int64]uint64
}

type specifiedResponseCacheEntry struct {
	response  SpecifiedResponse
	expiresAt time.Time
}

type specifiedResponseCacheLoad struct {
	done     chan struct{}
	response SpecifiedResponse
	err      error
	version  uint64
}

var errSpecifiedResponseCacheLoaderPanic = errors.New("specified response cache loader panicked")

func newSpecifiedResponseCache(ttl time.Duration) *specifiedResponseCache {
	if ttl <= 0 {
		ttl = specifiedResponseCacheTTL
	}
	return &specifiedResponseCache{
		ttl:     ttl,
		clock:   time.Now,
		items:   make(map[int64]specifiedResponseCacheEntry),
		loading: make(map[int64]*specifiedResponseCacheLoad),
		version: make(map[int64]uint64),
	}
}

func (c *specifiedResponseCache) get(now time.Time, userID int64) (SpecifiedResponse, bool) {
	if c == nil {
		return SpecifiedResponse{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.items[userID]
	if !ok || now.After(entry.expiresAt) {
		if ok {
			delete(c.items, userID)
		}
		return SpecifiedResponse{}, false
	}
	return entry.response, true
}

func (c *specifiedResponseCache) set(now time.Time, userID int64, response SpecifiedResponse) {
	if c == nil || userID <= 0 {
		return
	}
	c.mu.Lock()
	c.items[userID] = specifiedResponseCacheEntry{response: response, expiresAt: now.Add(c.ttl)}
	c.mu.Unlock()
}

func (c *specifiedResponseCache) invalidate(userID int64) {
	if c == nil || userID <= 0 {
		return
	}
	c.mu.Lock()
	delete(c.items, userID)
	c.version[userID]++
	delete(c.loading, userID)
	c.mu.Unlock()
}

func (c *specifiedResponseCache) getOrLoad(
	ctx context.Context,
	now time.Time,
	userID int64,
	loader func() (SpecifiedResponse, error),
) (SpecifiedResponse, error) {
	if c == nil {
		return loader()
	}
	for {
		if response, ok := c.get(now, userID); ok {
			return response, nil
		}

		c.mu.Lock()
		if entry, ok := c.items[userID]; ok && !now.After(entry.expiresAt) {
			c.mu.Unlock()
			return entry.response, nil
		}
		if call, ok := c.loading[userID]; ok {
			c.mu.Unlock()
			select {
			case <-call.done:
				c.mu.Lock()
				stale := call.version != c.version[userID]
				c.mu.Unlock()
				if stale {
					continue
				}
				return call.response, call.err
			case <-ctx.Done():
				return SpecifiedResponse{}, ctx.Err()
			}
		}
		call := &specifiedResponseCacheLoad{
			done:    make(chan struct{}),
			version: c.version[userID],
		}
		c.loading[userID] = call
		c.mu.Unlock()

		var panicValue any
		func() {
			defer func() { panicValue = recover() }()
			call.response, call.err = loader()
		}()
		if panicValue != nil {
			call.response = SpecifiedResponse{}
			call.err = errSpecifiedResponseCacheLoaderPanic
		}

		c.mu.Lock()
		stale := call.version != c.version[userID]
		if call.err == nil && !stale {
			c.items[userID] = specifiedResponseCacheEntry{
				response:  call.response,
				expiresAt: c.clock().Add(c.ttl),
			}
		}
		if c.loading[userID] == call {
			delete(c.loading, userID)
		}
		close(call.done)
		c.mu.Unlock()
		if panicValue != nil {
			panic(panicValue)
		}
		if stale {
			if err := ctx.Err(); err != nil {
				return SpecifiedResponse{}, err
			}
			continue
		}
		return call.response, call.err
	}
}
