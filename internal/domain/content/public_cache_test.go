package content

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func TestHomeOverviewLocalCacheAndInvalidation(t *testing.T) {
	now := time.Date(2026, time.July, 21, 10, 0, 0, 0, time.Local)
	service := &Service{
		publicCacheNow: func() time.Time { return now },
		publicCacheTTL: 5 * time.Second,
	}
	value := HomeOverview{Stats: PlatformStats{Calls24h: 12}}
	epoch := service.homeOverviewCacheEpoch()
	service.storeHomeOverview(context.Background(), value, epoch)

	got, ok := service.cachedHomeOverview(context.Background())
	if !ok || got.Stats.Calls24h != 12 {
		t.Fatalf("fresh local cache = %+v, %v", got, ok)
	}
	now = now.Add(6 * time.Second)
	if _, ok := service.cachedHomeOverview(context.Background()); ok {
		t.Fatal("expired local cache was treated as fresh")
	}
	stale, ok := service.staleHomeOverview()
	if !ok || stale.Stats.Calls24h != 12 {
		t.Fatalf("stale local cache = %+v, %v", stale, ok)
	}
	if _, ok := service.cachedHomeOverview(context.Background()); !ok {
		t.Fatal("stale retry window was not installed")
	}

	service.invalidateHomeOverview(context.Background())
	if _, ok := service.cachedHomeOverview(context.Background()); ok {
		t.Fatal("invalidated cache remained fresh")
	}
	if _, ok := service.staleHomeOverview(); ok {
		t.Fatal("invalidated cache remained available as stale")
	}
	if service.storeLocalHomeOverview(value, epoch) {
		t.Fatal("pre-invalidation load repopulated the cache")
	}
}

func TestSetPublicCacheInvalidatesLocalStateOnlyWhenConfigurationChanges(t *testing.T) {
	service := &Service{publicCacheTTL: 5 * time.Second}
	value := HomeOverview{Stats: PlatformStats{Calls24h: 23}}
	originalEpoch := service.homeOverviewCacheEpoch()
	if !service.storeLocalHomeOverview(value, originalEpoch) {
		t.Fatal("initial local cache store failed")
	}

	service.SetPublicCache(nil, 10*time.Second)
	changedEpoch := service.homeOverviewCacheEpoch()
	if changedEpoch == originalEpoch {
		t.Fatal("cache configuration change did not advance epoch")
	}
	if _, ok := service.cachedHomeOverview(context.Background()); ok {
		t.Fatal("cache configuration change retained old local data")
	}
	if service.storeLocalHomeOverview(value, originalEpoch) {
		t.Fatal("load from old cache configuration repopulated local data")
	}

	service.SetPublicCache(nil, 10*time.Second)
	if got := service.homeOverviewCacheEpoch(); got != changedEpoch {
		t.Fatalf("unchanged cache configuration advanced epoch: got %d want %d", got, changedEpoch)
	}
}

func TestInvalidateHomeOverviewDoesNotHoldLocalLockDuringRedisDelete(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	dialed := make(chan struct{})
	var dialOnce sync.Once
	client := redis.NewClient(&redis.Options{
		Addr: "blocked-redis",
		Dialer: func(context.Context, string, string) (net.Conn, error) {
			dialOnce.Do(func() { close(dialed) })
			return clientConn, nil
		},
		MaxRetries:   -1,
		ReadTimeout:  time.Second,
		WriteTimeout: time.Second,
	})
	t.Cleanup(func() {
		_ = serverConn.Close()
		_ = client.Close()
	})

	service := &Service{publicCache: client, publicCacheTTL: time.Minute}
	invalidateDone := make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() {
		service.invalidateHomeOverview(ctx)
		close(invalidateDone)
	}()

	select {
	case <-dialed:
	case <-time.After(time.Second):
		t.Fatal("Redis delete did not start")
	}

	localReadDone := make(chan struct{})
	go func() {
		_ = service.homeOverviewCacheEpoch()
		close(localReadDone)
	}()
	select {
	case <-localReadDone:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Redis delete blocked local home overview cache access")
	}

	cancel()
	_ = serverConn.Close()
	select {
	case <-invalidateDone:
	case <-time.After(time.Second):
		t.Fatal("cache invalidation did not stop after Redis connection closed")
	}
}

func TestHomeOverviewLoadSingleflight(t *testing.T) {
	service := &Service{}
	var calls atomic.Int64
	start := make(chan struct{})
	loader := func(context.Context, uint64) (HomeOverview, error) {
		calls.Add(1)
		<-start
		return HomeOverview{Stats: PlatformStats{Calls24h: 42}}, nil
	}

	const workers = 24
	results := make(chan HomeOverview, workers)
	errorsCh := make(chan error, workers)
	var ready sync.WaitGroup
	ready.Add(workers)
	for range workers {
		go func() {
			ready.Done()
			value, err := service.loadHomeOverviewSingleflight(context.Background(), loader)
			results <- value
			errorsCh <- err
		}()
	}
	ready.Wait()
	time.Sleep(20 * time.Millisecond)
	close(start)
	for range workers {
		if err := <-errorsCh; err != nil {
			t.Fatal(err)
		}
		if value := <-results; value.Stats.Calls24h != 42 {
			t.Fatalf("singleflight result = %+v", value)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("loader calls = %d, want 1", got)
	}
}

func TestHomeOverviewSingleflightDoesNotJoinPreviousEpoch(t *testing.T) {
	service := &Service{}
	oldStarted := make(chan struct{})
	releaseOld := make(chan struct{})
	type loadResult struct {
		value HomeOverview
		err   error
	}
	oldDone := make(chan loadResult, 1)
	go func() {
		value, err := service.loadHomeOverviewSingleflight(context.Background(), func(context.Context, uint64) (HomeOverview, error) {
			close(oldStarted)
			<-releaseOld
			return HomeOverview{Stats: PlatformStats{Calls24h: 1}}, nil
		})
		oldDone <- loadResult{value: value, err: err}
	}()

	select {
	case <-oldStarted:
	case <-time.After(time.Second):
		t.Fatal("old cache generation did not start loading")
	}
	service.invalidateHomeOverview(context.Background())

	newDone := make(chan loadResult, 1)
	go func() {
		value, err := service.loadHomeOverviewSingleflight(context.Background(), func(context.Context, uint64) (HomeOverview, error) {
			return HomeOverview{Stats: PlatformStats{Calls24h: 2}}, nil
		})
		newDone <- loadResult{value: value, err: err}
	}()
	select {
	case result := <-newDone:
		if result.err != nil || result.value.Stats.Calls24h != 2 {
			t.Fatalf("new cache generation result = %+v, %v", result.value, result.err)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("new cache generation joined the stale in-flight load")
	}

	close(releaseOld)
	select {
	case result := <-oldDone:
		if result.err != nil || result.value.Stats.Calls24h != 1 {
			t.Fatalf("old cache generation result = %+v, %v", result.value, result.err)
		}
	case <-time.After(time.Second):
		t.Fatal("old cache generation did not finish")
	}
}

func TestHomeOverviewLoaderPanicDoesNotStrandWaiters(t *testing.T) {
	service := &Service{}
	_, err := service.loadHomeOverviewSingleflight(context.Background(), func(context.Context, uint64) (HomeOverview, error) {
		panic("boom")
	})
	if !errors.Is(err, errHomeOverviewLoaderPanic) {
		t.Fatalf("panic error = %v", err)
	}
	value, err := service.loadHomeOverviewSingleflight(context.Background(), func(context.Context, uint64) (HomeOverview, error) {
		return HomeOverview{Stats: PlatformStats{Calls24h: 7}}, nil
	})
	if err != nil || value.Stats.Calls24h != 7 {
		t.Fatalf("subsequent load = %+v, %v", value, err)
	}
}

func TestHomeOverviewServesBoundedStaleDataWhenRefreshFails(t *testing.T) {
	now := time.Date(2026, time.July, 21, 12, 0, 0, 0, time.Local)
	service := &Service{
		publicCacheNow: func() time.Time { return now },
		publicCacheTTL: time.Second,
	}
	value := HomeOverview{Stats: PlatformStats{Calls24h: 88}}
	service.storeHomeOverview(context.Background(), value, service.homeOverviewCacheEpoch())
	now = now.Add(2 * time.Second)

	got, err := service.HomeOverview(context.Background())
	if err != nil || got.Stats.Calls24h != 88 {
		t.Fatalf("stale home overview = %+v, %v", got, err)
	}

	now = now.Add(homeOverviewStaleTTL)
	_, err = service.HomeOverview(context.Background())
	if !errors.Is(err, errHomeOverviewLoaderPanic) {
		t.Fatalf("expired stale cache error = %v", err)
	}
}
