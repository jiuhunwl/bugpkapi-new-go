package user

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestAdminWorkbenchStatsCacheCoalescesConcurrentSectionLoads(t *testing.T) {
	cache := newAdminWorkbenchStatsCache()
	started := make(chan struct{})
	release := make(chan struct{})
	var loads atomic.Int32

	loader := func(context.Context) (AdminWorkbenchStats, error) {
		loads.Add(1)
		close(started)
		<-release
		return AdminWorkbenchStats{Overview: AdminWorkbenchOverview{TotalUsers: 7}}, nil
	}

	const callers = 12
	results := make([]AdminWorkbenchStats, callers)
	errs := make([]error, callers)
	var wg sync.WaitGroup
	wg.Add(callers)
	for i := 0; i < callers; i++ {
		go func(index int) {
			defer wg.Done()
			results[index], errs[index] = cache.getOrLoad(
				context.Background(),
				"overview",
				loader,
			)
		}(i)
	}

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("cache loader did not start")
	}
	close(release)
	wg.Wait()

	if got := loads.Load(); got != 1 {
		t.Fatalf("loader calls = %d, want 1", got)
	}
	for i, err := range errs {
		if err != nil {
			t.Fatalf("caller %d returned error: %v", i, err)
		}
		if results[i].Overview.TotalUsers != 7 {
			t.Fatalf("caller %d result = %+v, want total users 7", i, results[i])
		}
	}
}

func TestAdminWorkbenchStatsCacheDoesNotShareDifferentSectionKeys(t *testing.T) {
	cache := newAdminWorkbenchStatsCache()
	var loads atomic.Int32
	loader := func(context.Context) (AdminWorkbenchStats, error) {
		loads.Add(1)
		return AdminWorkbenchStats{}, nil
	}

	if _, err := cache.getOrLoad(context.Background(), "overview", loader); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.getOrLoad(context.Background(), "overview,hot-apis", loader); err != nil {
		t.Fatal(err)
	}
	if got := loads.Load(); got != 2 {
		t.Fatalf("loader calls = %d, want one load per key", got)
	}
}

// Advance only deadlines, retaining all in-flight state, to exercise the normal
// expiry branch deterministically without sleeping through the real TTL.
func expireAdminWorkbenchCacheForTest(cache *adminWorkbenchStatsCache) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	for key, entry := range cache.entries {
		entry.expiresAt = time.Now().Add(-time.Minute)
		cache.entries[key] = entry
	}
}

func TestAdminWorkbenchStatsCacheReloadsAfterExpiry(t *testing.T) {
	cache := newAdminWorkbenchStatsCache()
	var loads atomic.Int32
	loader := func(context.Context) (AdminWorkbenchStats, error) {
		return AdminWorkbenchStats{AnalyticsReady: true, CallTrend7d: AdminCallTrend7d{Total: int64(loads.Add(1))}}, nil
	}
	for _, want := range []int64{1, 1} {
		value, err := cache.getOrLoad(context.Background(), "callTrend7d", loader)
		if err != nil || value.CallTrend7d.Total != want {
			t.Fatalf("healthy cache result=%+v, err=%v; want %d", value, err, want)
		}
	}
	expireAdminWorkbenchCacheForTest(cache)
	value, err := cache.getOrLoad(context.Background(), "callTrend7d", loader)
	if err != nil || value.CallTrend7d.Total != 2 || loads.Load() != 2 {
		t.Fatalf("expired cache pinned its completed request: total=%d loads=%d err=%v", value.CallTrend7d.Total, loads.Load(), err)
	}
}

func TestAdminWorkbenchStatsCacheRetriesUnavailableRefreshAfterExpiry(t *testing.T) {
	for _, degraded := range []bool{false, true} {
		name := "error"
		if degraded {
			name = "degraded"
		}
		t.Run(name, func(t *testing.T) {
			cache := newAdminWorkbenchStatsCache()
			var loads atomic.Int32
			unavailable := errors.New("statistics unavailable")
			loader := func(context.Context) (AdminWorkbenchStats, error) {
				switch loads.Add(1) {
				case 1:
					return AdminWorkbenchStats{AnalyticsReady: true, CallTrend7d: AdminCallTrend7d{Total: 1}}, nil
				case 2:
					if degraded {
						return AdminWorkbenchStats{DegradedSections: []AdminWorkbenchSection{AdminWorkbenchSectionCallTrend}}, nil
					}
					return AdminWorkbenchStats{}, unavailable
				default:
					return AdminWorkbenchStats{AnalyticsReady: true, CallTrend7d: AdminCallTrend7d{Total: 2}}, nil
				}
			}
			if _, err := cache.getOrLoad(context.Background(), "callTrend7d", loader); err != nil {
				t.Fatal(err)
			}
			expireAdminWorkbenchCacheForTest(cache)
			value, err := cache.getOrLoad(context.Background(), "callTrend7d", loader)
			if degraded {
				if err != nil || value.AnalyticsReady || len(value.DegradedSections) != 1 || value.DegradedSections[0] != AdminWorkbenchSectionCallTrend {
					t.Fatalf("degraded refresh hidden by old success: %+v, err=%v", value, err)
				}
			} else if !errors.Is(err, unavailable) {
				t.Fatalf("failed refresh hidden by old success: %+v, err=%v", value, err)
			}
			recovered, err := cache.getOrLoad(context.Background(), "callTrend7d", loader)
			if err != nil || !recovered.AnalyticsReady || recovered.CallTrend7d.Total != 2 || loads.Load() != 3 {
				t.Fatalf("unavailable response prevented recovery: %+v, loads=%d, err=%v", recovered, loads.Load(), err)
			}
		})
	}
}
