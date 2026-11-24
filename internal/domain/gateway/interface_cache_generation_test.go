package gateway

import (
	"context"
	"sync/atomic"
	"testing"
)

// A caller waiting on a stale in-flight load must retry after invalidation.
// Returning that snapshot once is enough to make a just-saved polling policy
// appear intermittent under concurrent traffic.
func TestInterfaceConfigCacheWaiterReloadsAfterInvalidation(t *testing.T) {
	cache := newInterfaceConfigCache(0)
	oldStarted := make(chan struct{})
	releaseOld := make(chan struct{})
	var loads atomic.Int32
	result := make(chan InterfaceConfig, 1)
	errResult := make(chan error, 1)

	go func() {
		value, err := cache.getOrLoad(context.Background(), cache.clock(), "weather", func() (InterfaceConfig, error) {
			if loads.Add(1) == 1 {
				close(oldStarted)
				<-releaseOld
				return InterfaceConfig{APICode: "weather", Name: "stale"}, nil
			}
			return InterfaceConfig{APICode: "weather", Name: "fresh"}, nil
		})
		result <- value
		errResult <- err
	}()

	<-oldStarted
	cache.invalidate("weather")
	close(releaseOld)

	select {
	case err := <-errResult:
		if err != nil {
			t.Fatal(err)
		}
	case value := <-result:
		if value.Name != "fresh" {
			t.Fatalf("stale in-flight snapshot escaped invalidation: %+v", value)
		}
	default:
	}

	select {
	case err := <-errResult:
		if err != nil {
			t.Fatal(err)
		}
	case value := <-result:
		if value.Name != "fresh" {
			t.Fatalf("stale in-flight snapshot escaped invalidation: %+v", value)
		}
	}
	if got := loads.Load(); got != 2 {
		t.Fatalf("loader calls = %d, want stale load plus one fresh reload", got)
	}
}
