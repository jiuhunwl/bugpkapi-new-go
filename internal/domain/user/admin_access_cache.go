package user

import (
	"context"
	"sync"
	"time"
)

const (
	adminAccessCacheTTL    = 5 * time.Second
	adminAccessLoadTimeout = 7 * time.Second
)

type adminAccessResult struct {
	Locations []AdminAccessLocation
	Summary   AdminAccessSummary
}

type adminAccessResultCall struct {
	done  chan struct{}
	err   error
	value adminAccessResult
}

type adminAccessResultCache struct {
	// 按范围分桶：四种范围（今天/7天/30天/全部）的结果互不相同，
	// 共用一个槽会让切换范围时读到上一个范围的数据。
	buckets map[AdminAccessRange]*adminAccessBucket
	mu      sync.Mutex
}

type adminAccessBucket struct {
	call      *adminAccessResultCall
	entry     adminAccessResult
	expiresAt time.Time
}

func newAdminAccessResultCache() *adminAccessResultCache {
	return &adminAccessResultCache{buckets: make(map[AdminAccessRange]*adminAccessBucket)}
}

func (s *Service) loadAdminAccess(ctx context.Context, rawRange string) (adminAccessResult, error) {
	cache := s.adminAccessCache
	if cache == nil {
		s.adminAccessCacheMu.Lock()
		cache = s.adminAccessCache
		if cache == nil {
			cache = newAdminAccessResultCache()
			s.adminAccessCache = cache
		}
		s.adminAccessCacheMu.Unlock()
	}
	rng := normalizeAdminAccessRange(rawRange)
	return cache.getOrLoad(ctx, rng, func(loadCtx context.Context) (adminAccessResult, error) {
		summary, locations, err := s.repo.AdminAccess(loadCtx, string(rng))
		return adminAccessResult{Locations: locations, Summary: summary}, err
	})
}

func (c *adminAccessResultCache) getOrLoad(ctx context.Context, rng AdminAccessRange, loader func(context.Context) (adminAccessResult, error)) (adminAccessResult, error) {
	if err := ctx.Err(); err != nil {
		return adminAccessResult{}, err
	}
	now := time.Now()
	c.mu.Lock()
	bucket := c.buckets[rng]
	if bucket == nil {
		bucket = &adminAccessBucket{}
		c.buckets[rng] = bucket
	}
	if now.Before(bucket.expiresAt) {
		value := bucket.entry
		c.mu.Unlock()
		return value, nil
	}
	if call := bucket.call; call != nil {
		c.mu.Unlock()
		select {
		case <-call.done:
			return call.value, call.err
		case <-ctx.Done():
			return adminAccessResult{}, ctx.Err()
		}
	}
	call := &adminAccessResultCall{done: make(chan struct{})}
	bucket.call = call
	c.mu.Unlock()

	go func() {
		loadCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), adminAccessLoadTimeout)
		defer cancel()
		value, err := loader(loadCtx)
		c.mu.Lock()
		call.value = value
		call.err = err
		if err == nil {
			// bucket 可能在加载期间被替换（同一范围不会，但保持防御性）。
			if current := c.buckets[rng]; current == bucket {
				bucket.entry = value
				bucket.expiresAt = time.Now().Add(adminAccessCacheTTL)
			}
		}
		if bucket.call == call {
			bucket.call = nil
		}
		close(call.done)
		c.mu.Unlock()
	}()

	select {
	case <-call.done:
		return call.value, call.err
	case <-ctx.Done():
		return adminAccessResult{}, ctx.Err()
	}
}
