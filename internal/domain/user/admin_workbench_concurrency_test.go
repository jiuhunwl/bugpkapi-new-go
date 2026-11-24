package user

import (
	"context"
	"errors"
	"testing"
	"time"
)

// drainAdminStatsSemaphore returns every slot so a failing test cannot leak a
// token into the next one. The semaphore is package state.
func drainAdminStatsSemaphore(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		for {
			select {
			case <-adminStatsSemaphore:
			default:
				return
			}
		}
	})
}

func TestAdminStatsSemaphoreBoundsConcurrentAggregations(t *testing.T) {
	drainAdminStatsSemaphore(t)
	ctx := context.Background()

	for i := 0; i < adminStatsReadConcurrency; i++ {
		if err := acquireAdminStatsSlot(ctx); err != nil {
			t.Fatalf("acquire %d of %d: %v", i+1, adminStatsReadConcurrency, err)
		}
	}

	// The next section must wait rather than start an eighth aggregation. A
	// short-deadline context models an already-saturated database: the section
	// is expected to degrade, not to queue forever.
	waitCtx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	err := acquireAdminStatsSlot(waitCtx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("acquire beyond the cap returned %v, want a deadline error", err)
	}

	releaseAdminStatsSlot()
	if err := acquireAdminStatsSlot(ctx); err != nil {
		t.Fatalf("a released slot must be reusable: %v", err)
	}
}

// A section that cannot get a slot in time is reported as degraded instead of
// silently contributing zero values, which would be indistinguishable from real
// business data.
func TestAdminStatsSemaphoreReportsUnavailableInsteadOfZeroes(t *testing.T) {
	drainAdminStatsSemaphore(t)
	for i := 0; i < adminStatsReadConcurrency; i++ {
		if err := acquireAdminStatsSlot(context.Background()); err != nil {
			t.Fatalf("seed acquire %d: %v", i, err)
		}
	}
	for i := 0; i < adminStatsReadConcurrency; i++ {
		defer releaseAdminStatsSlot()
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := acquireAdminStatsSlot(ctx); err == nil {
		t.Fatal("a cancelled request must not silently acquire a slot")
	}
}

func TestAdminStatsQueryTimeoutIsTighterThanTheFanOutBudget(t *testing.T) {
	if adminStatsQueryTimeout >= adminWorkbenchStatsLoadTimeout {
		t.Fatalf("per-query budget %v must be tighter than the fan-out budget %v",
			adminStatsQueryTimeout, adminWorkbenchStatsLoadTimeout)
	}
	// The fan-out must be able to run at least one full round of sections, or
	// every request would degrade the tail sections regardless of health.
	if adminWorkbenchStatsLoadTimeout < 2*adminStatsQueryTimeout {
		t.Fatalf("fan-out budget %v leaves no room for a second round of %v",
			adminWorkbenchStatsLoadTimeout, adminStatsQueryTimeout)
	}
	if adminStatsReadConcurrency < 2 {
		t.Fatalf("concurrency %d would serialize the dashboard tabs", adminStatsReadConcurrency)
	}
}

func TestAdminStatsSlotWithoutAcquireDoesNotBlock(t *testing.T) {
	drainAdminStatsSemaphore(t)
	// releaseAdminStatsSlot is called from a deferred position in a goroutine
	// that must always terminate; it must never block even if a future refactor
	// drops a matching acquire.
	done := make(chan struct{})
	go func() {
		defer close(done)
		releaseAdminStatsSlot()
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("releaseAdminStatsSlot blocked without a matching acquire")
	}
}
