package riskcontrol

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestResultCacheReusesValueUntilExpiry(t *testing.T) {
	now := time.Unix(1000, 0)
	cache := newResultCache[int](5*time.Second, 8, func() time.Time { return now })
	var loads int
	loader := func(context.Context) (int, error) {
		loads++
		return loads, nil
	}
	key := RiskQuery{Limit: 20, WindowMinutes: 10}

	first, err := cache.getOrLoad(context.Background(), key, loader)
	if err != nil || first != 1 {
		t.Fatalf("first load = %d, %v", first, err)
	}
	second, err := cache.getOrLoad(context.Background(), key, loader)
	if err != nil || second != 1 || loads != 1 {
		t.Fatalf("cached load = %d, loads=%d, err=%v", second, loads, err)
	}

	now = now.Add(6 * time.Second)
	third, err := cache.getOrLoad(context.Background(), key, loader)
	if err != nil || third != 2 || loads != 2 {
		t.Fatalf("expired load = %d, loads=%d, err=%v", third, loads, err)
	}
}

func TestResultCacheCoalescesConcurrentLoads(t *testing.T) {
	cache := newResultCache[int](5*time.Second, 8, time.Now)
	key := RiskQuery{Limit: 20, WindowMinutes: 10}
	var loads atomic.Int32
	release := make(chan struct{})
	start := make(chan struct{})
	const callers = 12

	results := make(chan int, callers)
	errorsCh := make(chan error, callers)
	var ready sync.WaitGroup
	var done sync.WaitGroup
	ready.Add(callers)
	done.Add(callers)
	for range callers {
		go func() {
			defer done.Done()
			ready.Done()
			<-start
			value, err := cache.getOrLoad(context.Background(), key, func(context.Context) (int, error) {
				loads.Add(1)
				<-release
				return 42, nil
			})
			results <- value
			errorsCh <- err
		}()
	}
	ready.Wait()
	close(start)
	time.Sleep(30 * time.Millisecond)
	close(release)
	done.Wait()

	if loads.Load() != 1 {
		t.Fatalf("loader called %d times, want 1", loads.Load())
	}
	close(results)
	close(errorsCh)
	for err := range errorsCh {
		if err != nil {
			t.Fatalf("concurrent load returned error: %v", err)
		}
	}
	for value := range results {
		if value != 42 {
			t.Fatalf("concurrent load value = %d, want 42", value)
		}
	}
}

func TestResultCacheDoesNotCacheErrors(t *testing.T) {
	cache := newResultCache[int](5*time.Second, 8, time.Now)
	key := RiskQuery{Limit: 20, WindowMinutes: 10}
	var loads int
	wantErr := errors.New("temporary database error")

	_, err := cache.getOrLoad(context.Background(), key, func(context.Context) (int, error) {
		loads++
		return 0, wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("first error = %v, want %v", err, wantErr)
	}
	value, err := cache.getOrLoad(context.Background(), key, func(context.Context) (int, error) {
		loads++
		return 7, nil
	})
	if err != nil || value != 7 || loads != 2 {
		t.Fatalf("retry value=%d loads=%d err=%v", value, loads, err)
	}
}

func TestResultCacheLoaderPanicReleasesWaiters(t *testing.T) {
	cache := newResultCache[int](5*time.Second, 8, time.Now)
	key := RiskQuery{Limit: 20, WindowMinutes: 10}
	started := make(chan struct{})
	release := make(chan struct{})
	leaderDone := make(chan error, 1)

	go func() {
		_, err := cache.getOrLoad(context.Background(), key, func(context.Context) (int, error) {
			close(started)
			<-release
			panic("database-driver-failure")
		})
		leaderDone <- err
	}()
	<-started

	waiterDone := make(chan error, 1)
	go func() {
		_, err := cache.getOrLoad(context.Background(), key, func(context.Context) (int, error) {
			t.Error("waiter should not execute a duplicate loader")
			return 0, nil
		})
		waiterDone <- err
	}()
	time.Sleep(20 * time.Millisecond)
	close(release)

	select {
	case err := <-waiterDone:
		if !errors.Is(err, errRiskCacheLoadPanic) {
			t.Fatalf("waiter error = %v, want %v", err, errRiskCacheLoadPanic)
		}
	case <-time.After(time.Second):
		t.Fatal("waiter remained blocked after loader panic")
	}
	select {
	case err := <-leaderDone:
		if !errors.Is(err, errRiskCacheLoadPanic) {
			t.Fatalf("leader error = %v, want %v", err, errRiskCacheLoadPanic)
		}
	case <-time.After(time.Second):
		t.Fatal("leader did not return after loader panic")
	}

	value, err := cache.getOrLoad(context.Background(), key, func(context.Context) (int, error) {
		return 9, nil
	})
	if err != nil || value != 9 {
		t.Fatalf("retry after panic value=%d err=%v", value, err)
	}
}

func TestResultCacheWaiterCancellationDoesNotCancelSharedLoad(t *testing.T) {
	cache := newResultCache[int](5*time.Second, 8, time.Now)
	key := RiskQuery{Limit: 20, WindowMinutes: 10}
	started := make(chan struct{})
	release := make(chan struct{})
	leaderDone := make(chan error, 1)

	go func() {
		_, err := cache.getOrLoad(context.Background(), key, func(ctx context.Context) (int, error) {
			close(started)
			select {
			case <-release:
				return 42, nil
			case <-ctx.Done():
				return 0, ctx.Err()
			}
		})
		leaderDone <- err
	}()
	<-started

	waiterCtx, cancelWaiter := context.WithCancel(context.Background())
	waiterDone := make(chan error, 1)
	go func() {
		_, err := cache.getOrLoad(waiterCtx, key, func(context.Context) (int, error) {
			t.Error("cancelled waiter executed a duplicate loader")
			return 0, nil
		})
		waiterDone <- err
	}()
	cancelWaiter()
	if err := <-waiterDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled waiter error = %v, want context.Canceled", err)
	}

	close(release)
	if err := <-leaderDone; err != nil {
		t.Fatalf("shared leader was cancelled by waiter: %v", err)
	}
	value, err := cache.getOrLoad(context.Background(), key, func(context.Context) (int, error) {
		t.Error("completed shared value was not cached")
		return 0, nil
	})
	if err != nil || value != 42 {
		t.Fatalf("cached shared value=%d err=%v", value, err)
	}
}

func TestResultCacheLeaderCancellationDoesNotBlockOrCancelSharedLoad(t *testing.T) {
	cache := newResultCache[int](5*time.Second, 8, time.Now)
	key := RiskQuery{Limit: 20, WindowMinutes: 10}
	started := make(chan struct{})
	release := make(chan struct{})
	var loads atomic.Int32

	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	leaderDone := make(chan error, 1)
	go func() {
		_, err := cache.getOrLoad(leaderCtx, key, func(ctx context.Context) (int, error) {
			loads.Add(1)
			close(started)
			select {
			case <-release:
				return 42, nil
			case <-ctx.Done():
				return 0, ctx.Err()
			}
		})
		leaderDone <- err
	}()
	<-started
	cancelLeader()

	select {
	case err := <-leaderDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled leader error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		close(release)
		t.Fatal("cancelled leader remained blocked on the shared load")
	}

	waiterDone := make(chan struct {
		err   error
		value int
	}, 1)
	go func() {
		value, err := cache.getOrLoad(context.Background(), key, func(context.Context) (int, error) {
			loads.Add(1)
			return 0, errors.New("duplicate loader executed")
		})
		waiterDone <- struct {
			err   error
			value int
		}{err: err, value: value}
	}()
	close(release)

	select {
	case result := <-waiterDone:
		if result.err != nil || result.value != 42 {
			t.Fatalf("shared load value=%d err=%v", result.value, result.err)
		}
	case <-time.After(time.Second):
		t.Fatal("waiter did not receive the completed shared load")
	}
	if loads.Load() != 1 {
		t.Fatalf("loader called %d times, want 1", loads.Load())
	}
}

func TestResultCacheEvictsOldestEntryAtCapacity(t *testing.T) {
	now := time.Unix(1000, 0)
	cache := newResultCache[int](time.Minute, 2, func() time.Time { return now })
	load := func(value int) func(context.Context) (int, error) {
		return func(context.Context) (int, error) { return value, nil }
	}
	first := RiskQuery{Limit: 10, WindowMinutes: 10}
	second := RiskQuery{Limit: 20, WindowMinutes: 10}
	third := RiskQuery{Limit: 30, WindowMinutes: 10}

	if _, err := cache.getOrLoad(context.Background(), first, load(1)); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	if _, err := cache.getOrLoad(context.Background(), second, load(2)); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	if _, err := cache.getOrLoad(context.Background(), third, load(3)); err != nil {
		t.Fatal(err)
	}

	cache.mu.Lock()
	defer cache.mu.Unlock()
	if len(cache.entries) != 2 {
		t.Fatalf("cache entries=%d, want 2", len(cache.entries))
	}
	if _, exists := cache.entries[first]; exists {
		t.Fatal("oldest cache entry was not evicted")
	}
	if _, exists := cache.entries[second]; !exists {
		t.Fatal("newer cache entry was unexpectedly evicted")
	}
	if _, exists := cache.entries[third]; !exists {
		t.Fatal("newest cache entry was unexpectedly evicted")
	}
}

// The homepage refresh calls the risk summary once a minute. With the
// interactive five-second TTL the entry always expired first, so the expensive
// 24-hour aggregate was re-run on every single refresh.
func TestHomeRiskCacheOutlivesInteractiveTTL(t *testing.T) {
	now := time.Unix(5000, 0)
	cache := newRiskHomeResultCache[int]()
	cache.now = func() time.Time { return now }
	var loads int
	loader := func(context.Context) (int, error) {
		loads++
		return loads, nil
	}
	key := RiskQuery{Limit: maxLimit, WindowMinutes: maxWindowMinutes}

	if _, err := cache.getOrLoad(context.Background(), key, loader); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	value, err := cache.getOrLoad(context.Background(), key, loader)
	if err != nil || value != 1 || loads != 1 {
		t.Fatalf("home cache expired within a refresh interval: value=%d loads=%d err=%v", value, loads, err)
	}
	now = now.Add(riskHomeCacheTTL)
	if _, err := cache.getOrLoad(context.Background(), key, loader); err != nil || loads != 2 {
		t.Fatalf("home cache never expires: loads=%d err=%v", loads, err)
	}
}

// The 24-hour aggregate takes tens of seconds on production data, so the
// homepage entry needs both a longer TTL and a longer loader deadline than the
// interactive caches. Sharing the interactive ten-second deadline aborted the
// aggregate before it could ever cache a value.
func TestHomeRiskCacheAllowsSlowerLoadsThanInteractiveCaches(t *testing.T) {
	home := newRiskHomeResultCache[int]()
	interactive := newRiskResultCache[int]()

	if interactive.loadTimeout != riskCacheLoadTimeout {
		t.Fatalf("interactive load timeout = %s, want %s", interactive.loadTimeout, riskCacheLoadTimeout)
	}
	if interactive.ttl != defaultRiskCacheTTL {
		t.Fatalf("interactive ttl = %s, want %s", interactive.ttl, defaultRiskCacheTTL)
	}
	if home.loadTimeout != riskHomeLoadTimeout {
		t.Fatalf("home load timeout = %s, want %s", home.loadTimeout, riskHomeLoadTimeout)
	}
	if home.ttl != riskHomeCacheTTL {
		t.Fatalf("home ttl = %s, want %s", home.ttl, riskHomeCacheTTL)
	}
	if riskHomeCacheTTL <= defaultRiskCacheTTL {
		t.Fatalf("home ttl %s must exceed the interactive %s", riskHomeCacheTTL, defaultRiskCacheTTL)
	}
	if riskHomeLoadTimeout <= riskCacheLoadTimeout {
		t.Fatalf("home load timeout %s must exceed the interactive %s", riskHomeLoadTimeout, riskCacheLoadTimeout)
	}
}

// A caller that runs out of budget must not cancel the shared load: the
// homepage gives up on its short refresh deadline while the aggregate keeps
// running (see getOrLoad's WithoutCancel) and warms the entry for the next
// refresh. Without that, a slow aggregate could never be cached at all.
func TestHomeRiskCacheWarmerSurvivesCallerCancellation(t *testing.T) {
	cache := newRiskHomeResultCache[int]()
	cache.loadTimeout = 5 * time.Second
	var loads atomic.Int32
	key := RiskQuery{Limit: maxLimit, WindowMinutes: maxWindowMinutes}

	callerCtx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	_, err := cache.getOrLoad(callerCtx, key, func(context.Context) (int, error) {
		loads.Add(1)
		time.Sleep(300 * time.Millisecond)
		return 7, nil
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("caller error = %v, want deadline exceeded", err)
	}

	value, err := cache.getOrLoad(context.Background(), key, func(context.Context) (int, error) {
		loads.Add(1)
		return 0, errors.New("cache was not warmed by the detached loader")
	})
	if err != nil {
		t.Fatalf("warmed read failed: %v", err)
	}
	if value != 7 {
		t.Fatalf("warmed value = %d, want 7", value)
	}
	if loads.Load() != 1 {
		t.Fatalf("loader ran %d times, want 1", loads.Load())
	}
}
