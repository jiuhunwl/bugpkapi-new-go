package riskcontrol

import (
	"context"
	"errors"
	"sync"
	"time"
)

const (
	defaultRiskCacheMaxEntries = 256
	defaultRiskCacheTTL        = 5 * time.Second
	riskCacheLoadTimeout       = 10 * time.Second

	// The homepage risk summary answers over the whole 24-hour detection window,
	// so it cannot share the interactive cache settings above.
	//
	// That projection aggregates every row of the five-minute fingerprint table.
	// It is an order of magnitude more expensive than the console's per-view
	// queries, and it used to be requested once a minute with a five-second TTL
	// and a ten-second load timeout: the entry always expired before the next
	// request, so the aggregate was re-run every minute and the load timeout
	// aborted it before it could ever produce a value. The homepage therefore
	// never saw a fresh risk summary and the server paid for a doomed aggregate
	// continuously.
	//
	// A five-minute TTL matches the risk overview refresh cadence, and the longer
	// load timeout lets the detached loader (see getOrLoad) actually finish and
	// warm the cache. Callers keep their own much shorter deadline, so a slow
	// aggregate delays nobody: the first refresh degrades and later ones read the
	// warm entry.
	riskHomeCacheTTL    = 5 * time.Minute
	riskHomeLoadTimeout = 90 * time.Second
)

var errRiskCacheLoadPanic = errors.New("risk-control cache loader panicked")

type cacheEntry[T any] struct {
	expiresAt time.Time
	value     T
}

type cacheCall[T any] struct {
	done  chan struct{}
	err   error
	value T
}

type resultCache[T any] struct {
	calls       map[RiskQuery]*cacheCall[T]
	entries     map[RiskQuery]cacheEntry[T]
	loadTimeout time.Duration
	maxEntries  int
	mu          sync.Mutex
	now         func() time.Time
	ttl         time.Duration
}

func (c *resultCache[T]) invalidate() {
	if c == nil {
		return
	}
	c.mu.Lock()
	for key := range c.entries {
		delete(c.entries, key)
	}
	c.mu.Unlock()
}

func newRiskResultCache[T any]() *resultCache[T] {
	return newResultCache[T](defaultRiskCacheTTL, defaultRiskCacheMaxEntries, time.Now)
}

// newRiskHomeResultCache builds the long-lived cache used by the homepage risk
// summary. See riskHomeCacheTTL for why it cannot share the interactive TTL and
// load timeout.
func newRiskHomeResultCache[T any]() *resultCache[T] {
	cache := newResultCache[T](riskHomeCacheTTL, defaultRiskCacheMaxEntries, time.Now)
	cache.loadTimeout = riskHomeLoadTimeout
	return cache
}

func newResultCache[T any](ttl time.Duration, maxEntries int, now func() time.Time) *resultCache[T] {
	if maxEntries <= 0 {
		maxEntries = defaultRiskCacheMaxEntries
	}
	if now == nil {
		now = time.Now
	}
	return &resultCache[T]{
		calls:       make(map[RiskQuery]*cacheCall[T]),
		entries:     make(map[RiskQuery]cacheEntry[T]),
		loadTimeout: riskCacheLoadTimeout,
		maxEntries:  maxEntries,
		now:         now,
		ttl:         ttl,
	}
}

func (c *resultCache[T]) getOrLoad(ctx context.Context, key RiskQuery, loader func(context.Context) (T, error)) (T, error) {
	var zero T
	if c == nil || c.ttl <= 0 {
		return loader(ctx)
	}
	if err := ctx.Err(); err != nil {
		return zero, err
	}

	now := c.now()
	c.mu.Lock()
	if entry, ok := c.entries[key]; ok {
		if now.Before(entry.expiresAt) {
			c.mu.Unlock()
			return entry.value, nil
		}
		delete(c.entries, key)
	}
	if call, ok := c.calls[key]; ok {
		c.mu.Unlock()
		select {
		case <-call.done:
			return call.value, call.err
		case <-ctx.Done():
			return zero, ctx.Err()
		}
	}
	call := &cacheCall[T]{done: make(chan struct{})}
	c.calls[key] = call
	c.mu.Unlock()

	go func() {
		loadCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.loadTimeout)
		defer cancel()
		value, err := runCacheLoader(loadCtx, loader)
		c.completeCall(key, call, value, err)
	}()

	select {
	case <-call.done:
		return call.value, call.err
	case <-ctx.Done():
		return zero, ctx.Err()
	}
}

func runCacheLoader[T any](ctx context.Context, loader func(context.Context) (T, error)) (value T, err error) {
	defer func() {
		if recover() != nil {
			var zero T
			value = zero
			err = errRiskCacheLoadPanic
		}
	}()
	return loader(ctx)
}

func (c *resultCache[T]) completeCall(key RiskQuery, call *cacheCall[T], value T, err error) {
	c.mu.Lock()
	call.value = value
	call.err = err
	if err == nil {
		c.storeLocked(key, value, c.now())
	}
	if current, ok := c.calls[key]; ok && current == call {
		delete(c.calls, key)
	}
	close(call.done)
	c.mu.Unlock()
}

func (c *resultCache[T]) storeLocked(key RiskQuery, value T, now time.Time) {
	for entryKey, entry := range c.entries {
		if !now.Before(entry.expiresAt) {
			delete(c.entries, entryKey)
		}
	}
	if len(c.entries) >= c.maxEntries {
		var oldestKey RiskQuery
		var oldestExpiry time.Time
		for entryKey, entry := range c.entries {
			if oldestExpiry.IsZero() || entry.expiresAt.Before(oldestExpiry) {
				oldestKey = entryKey
				oldestExpiry = entry.expiresAt
			}
		}
		delete(c.entries, oldestKey)
	}
	c.entries[key] = cacheEntry[T]{expiresAt: now.Add(c.ttl), value: value}
}
