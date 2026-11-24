package gateway

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSpecifiedResponseCacheCoalescesConcurrentLoads(t *testing.T) {
	cache := newSpecifiedResponseCache(time.Minute)
	const workers = 32
	var loads atomic.Int32
	start := make(chan struct{})
	var wait sync.WaitGroup
	results := make(chan SpecifiedResponse, workers)
	errors := make(chan error, workers)

	for index := 0; index < workers; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			value, err := cache.getOrLoad(context.Background(), time.Now(), 9, func() (SpecifiedResponse, error) {
				loads.Add(1)
				<-start
				return SpecifiedResponse{Enabled: 1, Billable: 1, Body: `{"ok":true}`}, nil
			})
			if err != nil {
				errors <- err
				return
			}
			results <- value
		}()
	}

	for loads.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	close(start)
	wait.Wait()
	close(results)
	close(errors)

	if loads.Load() != 1 {
		t.Fatalf("specified response loads = %d, want 1", loads.Load())
	}
	for err := range errors {
		t.Fatalf("coalesced specified response load returned error: %v", err)
	}
	count := 0
	for value := range results {
		count++
		if value.Enabled != 1 || value.Billable != 1 || value.Body != `{"ok":true}` {
			t.Fatalf("unexpected specified response: %+v", value)
		}
	}
	if count != workers {
		t.Fatalf("coalesced result count = %d, want %d", count, workers)
	}
}

func TestSpecifiedResponseCacheTTLStartsAfterLoadCompletes(t *testing.T) {
	cache := newSpecifiedResponseCache(time.Second)
	startedAt := time.Now()
	completedAt := startedAt.Add(5 * time.Second)
	cache.clock = func() time.Time { return completedAt }

	_, err := cache.getOrLoad(context.Background(), startedAt, 9, func() (SpecifiedResponse, error) {
		return SpecifiedResponse{Enabled: 1, Body: `{"ok":true}`}, nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, ok := cache.get(completedAt.Add(500*time.Millisecond), 9); !ok {
		t.Fatal("specified response cache should remain valid for the full TTL after loading")
	}
	if _, ok := cache.get(completedAt.Add(time.Second+time.Millisecond), 9); ok {
		t.Fatal("specified response cache should expire after the completion-based TTL")
	}
}

func TestSpecifiedResponseCacheInvalidatesOneUser(t *testing.T) {
	cache := newSpecifiedResponseCache(time.Minute)
	now := time.Now()
	cache.set(now, 9, SpecifiedResponse{Enabled: 1, Body: `{"version":1}`})
	cache.set(now, 10, SpecifiedResponse{Enabled: 1, Body: `{"version":1}`})

	cache.invalidate(9)
	if _, ok := cache.get(now, 9); ok {
		t.Fatal("invalidated specified response remained cached")
	}
	if _, ok := cache.get(now, 10); !ok {
		t.Fatal("invalidating one user removed another user's specified response")
	}
}
