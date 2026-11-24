package content

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	homeOverviewCacheKey         = "content:public:home-overview:v2"
	homeOverviewLoadTimeout      = 10 * time.Second
	homeOverviewStaleTTL         = 5 * time.Minute
	homeOverviewStaleRetryWindow = 5 * time.Second
)

var errHomeOverviewLoaderPanic = errors.New("home overview loader panicked")

type homeOverviewCacheEntry struct {
	expiresAt time.Time
	staleAt   time.Time
	value     HomeOverview
}

type homeOverviewCall struct {
	done  chan struct{}
	epoch uint64
	err   error
	value HomeOverview
}

func (s *Service) SetPublicCache(client *redis.Client, ttl time.Duration) {
	s.publicCacheMu.Lock()
	nextTTL := s.publicCacheTTL
	if ttl > 0 {
		nextTTL = ttl
	}
	if s.publicCache != client || s.publicCacheTTL != nextTTL {
		s.publicHomeEpoch++
		s.publicHomeData = homeOverviewCacheEntry{}
	}
	s.publicCache = client
	s.publicCacheTTL = nextTTL
	s.publicCacheMu.Unlock()
}

func (s *Service) cachedHomeOverview(ctx context.Context) (HomeOverview, bool) {
	now := s.publicCacheTime()
	s.publicCacheMu.Lock()
	if s.publicCacheTTL <= 0 {
		s.publicCacheMu.Unlock()
		return HomeOverview{}, false
	}
	if now.Before(s.publicHomeData.expiresAt) {
		value := s.publicHomeData.value
		s.publicCacheMu.Unlock()
		return value, true
	}
	cache := s.publicCache
	epoch := s.publicHomeEpoch
	s.publicCacheMu.Unlock()
	if cache == nil {
		return HomeOverview{}, false
	}
	raw, err := cache.Get(ctx, homeOverviewCacheKey).Bytes()
	if err != nil {
		return HomeOverview{}, false
	}
	var result HomeOverview
	if json.Unmarshal(raw, &result) != nil {
		return HomeOverview{}, false
	}
	if !s.storeLocalHomeOverview(result, epoch) {
		return HomeOverview{}, false
	}
	return result, true
}

func (s *Service) storeHomeOverview(ctx context.Context, result HomeOverview, epoch uint64) {
	if !s.storeLocalHomeOverview(result, epoch) {
		return
	}
	cache, ttl, ok := s.publicCacheSnapshot(epoch)
	if !ok || cache == nil {
		return
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return
	}
	_ = cache.Set(ctx, homeOverviewCacheKey, raw, ttl).Err()
}

func (s *Service) invalidateHomeOverview(ctx context.Context) {
	s.publicCacheMu.Lock()
	s.publicHomeEpoch++
	s.publicHomeData = homeOverviewCacheEntry{}
	cache := s.publicCache
	s.publicCacheMu.Unlock()
	if cache != nil {
		_ = cache.Del(ctx, homeOverviewCacheKey).Err()
	}
}

// InvalidateHomeOverview removes cached business analytics after an account
// ban state changes, then coalesces a background snapshot refresh. The
// account mutation itself never waits for the aggregate queries.
func (s *Service) InvalidateHomeOverview(ctx context.Context) {
	s.invalidateHomeOverview(ctx)
	s.queueHomeAnalyticsSnapshotRefresh()
}

func (s *Service) queueHomeAnalyticsSnapshotRefresh() {
	wake := s.ensureHomeAnalyticsSnapshotWake()
	select {
	case wake <- struct{}{}:
	default:
	}
}

func (s *Service) ensureHomeAnalyticsSnapshotWake() chan struct{} {
	s.publicCacheMu.Lock()
	defer s.publicCacheMu.Unlock()
	if s.homeSnapshotWake == nil {
		s.homeSnapshotWake = make(chan struct{}, 1)
	}
	return s.homeSnapshotWake
}

func (s *Service) storeLocalHomeOverview(result HomeOverview, epoch uint64) bool {
	now := s.publicCacheTime()
	s.publicCacheMu.Lock()
	defer s.publicCacheMu.Unlock()
	if epoch != s.publicHomeEpoch || s.publicCacheTTL <= 0 {
		return false
	}
	s.publicHomeData = homeOverviewCacheEntry{
		expiresAt: now.Add(s.publicCacheTTL),
		staleAt:   now.Add(homeOverviewStaleTTL),
		value:     result,
	}
	return true
}

func (s *Service) publicCacheSnapshot(epoch uint64) (*redis.Client, time.Duration, bool) {
	s.publicCacheMu.Lock()
	defer s.publicCacheMu.Unlock()
	if epoch != s.publicHomeEpoch || s.publicCacheTTL <= 0 {
		return nil, 0, false
	}
	return s.publicCache, s.publicCacheTTL, true
}

func (s *Service) staleHomeOverview() (HomeOverview, bool) {
	now := s.publicCacheTime()
	s.publicCacheMu.Lock()
	defer s.publicCacheMu.Unlock()
	if s.publicHomeData.staleAt.IsZero() || !now.Before(s.publicHomeData.staleAt) {
		return HomeOverview{}, false
	}
	s.publicHomeData.expiresAt = now.Add(homeOverviewStaleRetryWindow)
	return s.publicHomeData.value, true
}

func (s *Service) homeOverviewCacheEpoch() uint64 {
	s.publicCacheMu.Lock()
	defer s.publicCacheMu.Unlock()
	return s.publicHomeEpoch
}

func (s *Service) publicCacheTime() time.Time {
	if s.publicCacheNow != nil {
		return s.publicCacheNow()
	}
	return time.Now()
}

func (s *Service) loadHomeOverviewSingleflight(ctx context.Context, loader func(context.Context, uint64) (HomeOverview, error)) (HomeOverview, error) {
	s.publicCacheMu.Lock()
	epoch := s.publicHomeEpoch
	if call := s.publicHomeCall; call != nil && call.epoch == epoch {
		s.publicCacheMu.Unlock()
		select {
		case <-call.done:
			return call.value, call.err
		case <-ctx.Done():
			return HomeOverview{}, ctx.Err()
		}
	}
	call := &homeOverviewCall{done: make(chan struct{}), epoch: epoch}
	s.publicHomeCall = call
	s.publicCacheMu.Unlock()

	loadCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), homeOverviewLoadTimeout)
	value, err := runHomeOverviewLoader(loadCtx, epoch, loader)
	cancel()

	s.publicCacheMu.Lock()
	call.value = value
	call.err = err
	if s.publicHomeCall == call {
		s.publicHomeCall = nil
	}
	close(call.done)
	s.publicCacheMu.Unlock()
	return value, err
}

func runHomeOverviewLoader(ctx context.Context, epoch uint64, loader func(context.Context, uint64) (HomeOverview, error)) (value HomeOverview, err error) {
	defer func() {
		if recover() != nil {
			value = HomeOverview{}
			err = errHomeOverviewLoaderPanic
		}
	}()
	return loader(ctx, epoch)
}
