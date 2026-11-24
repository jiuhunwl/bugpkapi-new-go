package ticket

import (
	"context"
	"fmt"
	"sync"
	"time"
)

const ticketReadCacheTTL = 2 * time.Second

type ticketReadCache struct {
	mu      sync.Mutex
	ttl     time.Duration
	epoch   uint64
	items   map[string]ticketReadCacheEntry
	loading map[string]*ticketReadCacheLoad
}

type ticketReadCacheEntry struct {
	expiresAt time.Time
	value     any
}

type ticketReadCacheLoad struct {
	done  chan struct{}
	epoch uint64
	err   error
	value any
}

func newTicketReadCache(ttl time.Duration) *ticketReadCache {
	if ttl <= 0 {
		ttl = ticketReadCacheTTL
	}
	return &ticketReadCache{
		ttl:     ttl,
		items:   make(map[string]ticketReadCacheEntry),
		loading: make(map[string]*ticketReadCacheLoad),
	}
}

func (c *ticketReadCache) getOrLoad(
	ctx context.Context,
	key string,
	loader func() (any, error),
) (any, error) {
	if c == nil {
		return loader()
	}
	for {
		now := time.Now()
		c.mu.Lock()
		if entry, ok := c.items[key]; ok && !now.After(entry.expiresAt) {
			c.mu.Unlock()
			return entry.value, nil
		}
		if call, ok := c.loading[key]; ok {
			c.mu.Unlock()
			select {
			case <-call.done:
				c.mu.Lock()
				stale := call.epoch != c.epoch
				c.mu.Unlock()
				if stale {
					if err := ctx.Err(); err != nil {
						return nil, err
					}
					continue
				}
				return call.value, call.err
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		call := &ticketReadCacheLoad{done: make(chan struct{}), epoch: c.epoch}
		c.loading[key] = call
		c.mu.Unlock()

		func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					call.err = fmt.Errorf("ticket read cache loader panic: %v", recovered)
				}
			}()
			call.value, call.err = loader()
		}()

		c.mu.Lock()
		stale := call.epoch != c.epoch
		if call.err == nil && !stale {
			c.items[key] = ticketReadCacheEntry{
				expiresAt: time.Now().Add(c.ttl),
				value:     call.value,
			}
		}
		if c.loading[key] == call {
			delete(c.loading, key)
		}
		close(call.done)
		c.mu.Unlock()
		if stale {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			continue
		}
		return call.value, call.err
	}
}

func (c *ticketReadCache) clear() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.epoch++
	c.items = make(map[string]ticketReadCacheEntry)
	c.loading = make(map[string]*ticketReadCacheLoad)
	c.mu.Unlock()
}

func (r *Repository) invalidateReadCache() {
	if r != nil {
		r.readCache.clear()
	}
}
