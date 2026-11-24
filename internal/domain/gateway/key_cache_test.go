package gateway

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestKeyAuthCacheCoalescesConcurrentLoads(t *testing.T) {
	cache := newKeyAuthCache(time.Minute)
	const workers = 32
	var loads atomic.Int32
	start := make(chan struct{})
	var wait sync.WaitGroup
	results := make(chan KeyAuth, workers)
	errors := make(chan error, workers)

	for index := 0; index < workers; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			value, err := cache.getOrLoad(context.Background(), time.Unix(100, 0), "hash", func() (KeyAuth, error) {
				loads.Add(1)
				<-start
				return KeyAuth{ID: 7, UserID: 9, Status: 1}, nil
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
		t.Fatalf("key auth loads = %d, want 1", loads.Load())
	}
	for err := range errors {
		t.Fatalf("coalesced key auth load returned error: %v", err)
	}
	count := 0
	for value := range results {
		count++
		if value.ID != 7 || value.UserID != 9 || value.Status != 1 {
			t.Fatalf("unexpected key auth: %+v", value)
		}
	}
	if count != workers {
		t.Fatalf("key auth result count = %d, want %d", count, workers)
	}
}

func TestKeyAuthCacheExpiresAndInvalidatesByKeyID(t *testing.T) {
	cache := newKeyAuthCache(time.Second)
	base := time.Unix(200, 0)
	cache.set(base, "hash", KeyAuth{ID: 11, Status: 1})

	if _, ok := cache.get(base.Add(500*time.Millisecond), "hash"); !ok {
		t.Fatal("expected a key auth cache hit before expiry")
	}
	if _, ok := cache.get(base.Add(2*time.Second), "hash"); ok {
		t.Fatal("expected a key auth cache miss after expiry")
	}

	cache.set(base, "hash", KeyAuth{ID: 11, Status: 1})
	cache.invalidateKey(11)
	if _, ok := cache.get(base, "hash"); ok {
		t.Fatal("invalidated key auth entry remained cached")
	}
}

func TestKeyInterfaceCacheCoalescesAndInvalidates(t *testing.T) {
	cache := newKeyInterfaceCache(time.Minute)
	var loads atomic.Int32
	allowed, err := cache.allows(context.Background(), time.Unix(300, 0), 5, 8, func() ([]int64, error) {
		loads.Add(1)
		return []int64{8, 13}, nil
	})
	if err != nil || !allowed {
		t.Fatalf("first interface permission = %v, %v", allowed, err)
	}
	allowed, err = cache.allows(context.Background(), time.Unix(300, 0), 5, 13, func() ([]int64, error) {
		loads.Add(1)
		return []int64{8, 13}, nil
	})
	if err != nil || !allowed {
		t.Fatalf("cached interface permission = %v, %v", allowed, err)
	}
	if loads.Load() != 1 {
		t.Fatalf("interface permission loads = %d, want 1", loads.Load())
	}

	cache.invalidateKey(5)
	allowed, err = cache.allows(context.Background(), time.Unix(300, 0), 5, 13, func() ([]int64, error) {
		loads.Add(1)
		return []int64{8}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if allowed {
		t.Fatal("interface permission cache returned stale binding after invalidation")
	}
	if loads.Load() != 2 {
		t.Fatalf("interface permission reloads = %d, want 2", loads.Load())
	}
}

func TestKeyAuthCacheInvalidationDoesNotRepopulateFromOlderLoad(t *testing.T) {
	cache := newKeyAuthCache(time.Minute)
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	firstDone := make(chan KeyAuth, 1)

	go func() {
		value, _ := cache.getOrLoad(context.Background(), time.Unix(400, 0), "hash", func() (KeyAuth, error) {
			close(firstStarted)
			<-releaseFirst
			return KeyAuth{ID: 7, Status: 1}, nil
		})
		firstDone <- value
	}()
	<-firstStarted

	cache.invalidateKey(7)
	newer, err := cache.getOrLoad(context.Background(), time.Unix(401, 0), "hash", func() (KeyAuth, error) {
		return KeyAuth{ID: 7, Status: 0}, nil
	})
	if err != nil || newer.Status != 0 {
		t.Fatalf("post-invalidation key auth = %+v, %v", newer, err)
	}
	close(releaseFirst)
	olderCallerResult := <-firstDone
	if olderCallerResult.Status != 0 {
		t.Fatalf("in-flight key auth caller returned stale status: %+v", olderCallerResult)
	}

	cached, ok := cache.get(time.Unix(402, 0), "hash")
	if !ok || cached.Status != 0 {
		t.Fatalf("older load overwrote newer key auth: %+v, %v", cached, ok)
	}
}

func TestKeyAuthCacheTTLStartsAfterLoadCompletes(t *testing.T) {
	cache := newKeyAuthCache(time.Second)
	startedAt := time.Now()
	completedAt := startedAt.Add(5 * time.Second)
	cache.clock = func() time.Time { return completedAt }

	_, err := cache.getOrLoad(context.Background(), startedAt, "hash", func() (KeyAuth, error) {
		return KeyAuth{ID: 7, Status: 1}, nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, ok := cache.get(completedAt.Add(500*time.Millisecond), "hash"); !ok {
		t.Fatal("key auth cache should remain valid for the full TTL after loading")
	}
	if _, ok := cache.get(completedAt.Add(time.Second+time.Millisecond), "hash"); ok {
		t.Fatal("key auth cache should expire after the completion-based TTL")
	}
}

func TestKeyInterfaceCacheInvalidationDoesNotRepopulateFromOlderLoad(t *testing.T) {
	cache := newKeyInterfaceCache(time.Minute)
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	firstDone := make(chan bool, 1)

	go func() {
		allowed, _ := cache.allows(context.Background(), time.Unix(500, 0), 5, 13, func() ([]int64, error) {
			close(firstStarted)
			<-releaseFirst
			return []int64{8, 13}, nil
		})
		firstDone <- allowed
	}()
	<-firstStarted

	cache.invalidateKey(5)
	allowed, err := cache.allows(context.Background(), time.Unix(501, 0), 5, 13, func() ([]int64, error) {
		return []int64{8}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if allowed {
		t.Fatal("post-invalidation interface binding remained allowed")
	}
	close(releaseFirst)
	if staleAllowed := <-firstDone; staleAllowed {
		t.Fatal("in-flight interface permission caller returned stale binding")
	}

	entry, ok := cache.get(time.Unix(502, 0), 5)
	if !ok {
		t.Fatal("newer interface permission entry was not cached")
	}
	if _, stale := entry.interfaces[13]; stale {
		t.Fatal("older interface load overwrote newer binding set")
	}
}

func TestKeyInterfaceCacheTTLStartsAfterLoadCompletes(t *testing.T) {
	cache := newKeyInterfaceCache(time.Second)
	startedAt := time.Now()
	completedAt := startedAt.Add(5 * time.Second)
	cache.clock = func() time.Time { return completedAt }

	allowed, err := cache.allows(context.Background(), startedAt, 5, 8, func() ([]int64, error) {
		return []int64{8}, nil
	})
	if err != nil || !allowed {
		t.Fatalf("initial interface permission = %v, %v", allowed, err)
	}

	if _, ok := cache.get(completedAt.Add(500*time.Millisecond), 5); !ok {
		t.Fatal("key interface cache should remain valid for the full TTL after loading")
	}
	if _, ok := cache.get(completedAt.Add(time.Second+time.Millisecond), 5); ok {
		t.Fatal("key interface cache should expire after the completion-based TTL")
	}
}
