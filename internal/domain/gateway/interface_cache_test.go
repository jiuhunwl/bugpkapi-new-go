package gateway

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"bugpk-api/internal/pkg/proxypool"
)

func TestInterfaceConfigCacheHitAndMiss(t *testing.T) {
	cache := newInterfaceConfigCache(time.Minute)
	now := time.Now()
	config := InterfaceConfig{APICode: "weather", Name: "Weather", Price: "0.1000"}

	if _, ok := cache.get(now, "weather"); ok {
		t.Fatal("expected a miss before the entry is set")
	}
	cache.set(now, "weather", config)

	got, ok := cache.get(now, "weather")
	if !ok || got.APICode != "weather" || got.Name != "Weather" || got.Price != "0.1000" {
		t.Fatalf("cache hit = %+v, %v", got, ok)
	}
}

func TestInterfaceConfigCacheExpires(t *testing.T) {
	cache := newInterfaceConfigCache(time.Minute)
	now := time.Now()
	cache.set(now, "weather", InterfaceConfig{APICode: "weather"})

	if _, ok := cache.get(now.Add(time.Minute-1), "weather"); !ok {
		t.Fatal("expected a hit before expiry")
	}
	if _, ok := cache.get(now.Add(time.Minute+time.Second), "weather"); ok {
		t.Fatal("expected a miss after expiry")
	}
}

func TestInterfaceConfigCacheReturnsCopy(t *testing.T) {
	cache := newInterfaceConfigCache(time.Minute)
	now := time.Now()
	config := InterfaceConfig{APICode: "weather", ProxyPoolConfig: proxypool.Config{Password: "ciphertext"}}
	cache.set(now, "weather", config)

	got, ok := cache.get(now, "weather")
	if !ok {
		t.Fatal("expected a cache hit")
	}
	got.ProxyPoolConfig.Password = "decrypted"

	again, ok := cache.get(now, "weather")
	if !ok || again.ProxyPoolConfig.Password != "ciphertext" {
		t.Fatalf("cached entry was mutated: %+v", again.ProxyPoolConfig)
	}
}

func TestInterfaceConfigCacheInvalidatesByAPICode(t *testing.T) {
	cache := newInterfaceConfigCache(time.Minute)
	now := time.Now()
	cache.set(now, "weather", InterfaceConfig{APICode: "weather"})
	cache.set(now, "search", InterfaceConfig{APICode: "search"})

	cache.invalidate("weather")
	if _, ok := cache.get(now, "weather"); ok {
		t.Fatal("invalidated interface configuration was still cached")
	}
	if _, ok := cache.get(now, "search"); !ok {
		t.Fatal("invalidating one interface removed an unrelated cache entry")
	}
}

func TestInterfaceConfigCacheInvalidatesAPICodeCaseInsensitively(t *testing.T) {
	cache := newInterfaceConfigCache(time.Minute)
	now := time.Now()
	cache.set(now, "Weather", InterfaceConfig{APICode: "Weather"})

	if _, ok := cache.get(now, "Weather"); !ok {
		t.Fatal("expected the mixed-case cache entry to exist before invalidation")
	}
	cache.invalidate("weather")
	if _, ok := cache.get(now, "Weather"); ok {
		t.Fatal("lower-case invalidation left the mixed-case API code cached")
	}
}

func TestInterfaceConfigCacheGetOrLoadUsesCaseInsensitiveKey(t *testing.T) {
	cache := newInterfaceConfigCache(time.Minute)
	var loads atomic.Int32

	first, err := cache.getOrLoad(context.Background(), time.Now(), "Weather", func() (InterfaceConfig, error) {
		loads.Add(1)
		return InterfaceConfig{APICode: "Weather", Name: "Weather API"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := cache.getOrLoad(context.Background(), time.Now(), "weather", func() (InterfaceConfig, error) {
		loads.Add(1)
		return InterfaceConfig{APICode: "weather", Name: "unexpected reload"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if loads.Load() != 1 {
		t.Fatalf("case-only API code change triggered %d loads, want 1", loads.Load())
	}
	if first.Name != "Weather API" || second.Name != "Weather API" {
		t.Fatalf("case-insensitive cache values = first:%+v second:%+v", first, second)
	}
}

func TestInterfaceConfigCacheWildcardInvalidationStillClearsAllKeys(t *testing.T) {
	cache := newInterfaceConfigCache(time.Minute)
	now := time.Now()
	cache.set(now, "Weather", InterfaceConfig{APICode: "Weather"})
	cache.set(now, "search", InterfaceConfig{APICode: "search"})

	cache.invalidate(" * ")
	if _, ok := cache.get(now, "Weather"); ok {
		t.Fatal("wildcard invalidation left the mixed-case API code cached")
	}
	if _, ok := cache.get(now, "search"); ok {
		t.Fatal("wildcard invalidation left an unrelated API code cached")
	}
}

func TestInterfaceConfigCacheCoalescesConcurrentLoads(t *testing.T) {
	cache := newInterfaceConfigCache(time.Minute)
	const workers = 32
	var loads atomic.Int32
	start := make(chan struct{})
	var wait sync.WaitGroup
	results := make(chan InterfaceConfig, workers)
	errors := make(chan error, workers)

	for index := 0; index < workers; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			value, err := cache.getOrLoad(context.Background(), time.Now(), "weather", func() (InterfaceConfig, error) {
				loads.Add(1)
				<-start
				return InterfaceConfig{APICode: "weather", Name: "Weather"}, nil
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
		t.Fatalf("concurrent config loads = %d, want 1", loads.Load())
	}
	for err := range errors {
		t.Fatalf("coalesced load returned error: %v", err)
	}
	count := 0
	for value := range results {
		count++
		if value.APICode != "weather" || value.Name != "Weather" {
			t.Fatalf("unexpected coalesced value: %+v", value)
		}
	}
	if count != workers {
		t.Fatalf("coalesced result count = %d, want %d", count, workers)
	}
}

func TestInterfaceConfigCacheTTLStartsAfterLoadCompletes(t *testing.T) {
	cache := newInterfaceConfigCache(time.Second)
	startedAt := time.Now()
	completedAt := startedAt.Add(5 * time.Second)
	cache.clock = func() time.Time { return completedAt }

	_, err := cache.getOrLoad(context.Background(), startedAt, "weather", func() (InterfaceConfig, error) {
		return InterfaceConfig{APICode: "weather"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, ok := cache.get(completedAt.Add(500*time.Millisecond), "weather"); !ok {
		t.Fatal("cache should remain valid for the full TTL after a slow load completes")
	}
	if _, ok := cache.get(completedAt.Add(time.Second+time.Millisecond), "weather"); ok {
		t.Fatal("cache should expire after the completion-based TTL")
	}
}

func TestInterfaceConfigCacheInvalidationWinsAgainstInFlightLoad(t *testing.T) {
	cache := newInterfaceConfigCache(time.Minute)
	loadStarted := make(chan struct{})
	releaseLoad := make(chan struct{})
	firstResult := make(chan InterfaceConfig, 1)
	firstError := make(chan error, 1)
	var loads atomic.Int32

	go func() {
		value, err := cache.getOrLoad(context.Background(), time.Now(), "weather", func() (InterfaceConfig, error) {
			if loads.Add(1) == 1 {
				close(loadStarted)
				<-releaseLoad
				return InterfaceConfig{APICode: "weather", Name: "stale"}, nil
			}
			return InterfaceConfig{APICode: "weather", Name: "fresh"}, nil
		})
		firstResult <- value
		firstError <- err
	}()

	<-loadStarted
	cache.invalidate("weather")
	close(releaseLoad)
	if err := <-firstError; err != nil {
		t.Fatal(err)
	}
	if got := <-firstResult; got.Name != "fresh" {
		t.Fatalf("in-flight caller returned a stale snapshot after invalidation: %+v", got)
	}

	got, err := cache.getOrLoad(context.Background(), time.Now(), "weather", func() (InterfaceConfig, error) {
		loads.Add(1)
		return InterfaceConfig{APICode: "weather", Name: "fresh"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if loads.Load() != 2 || got.Name != "fresh" {
		t.Fatalf("invalidation allowed stale load to repopulate cache: loads=%d result=%+v", loads.Load(), got)
	}
}

func TestInterfaceConfigCacheStartsFreshGenerationBeforeOldLoadCompletes(t *testing.T) {
	cache := newInterfaceConfigCache(time.Minute)
	oldStarted := make(chan struct{})
	releaseOld := make(chan struct{})
	oldDone := make(chan struct{})
	go func() {
		defer close(oldDone)
		_, _ = cache.getOrLoad(context.Background(), time.Now(), "weather", func() (InterfaceConfig, error) {
			close(oldStarted)
			<-releaseOld
			return InterfaceConfig{APICode: "weather", Name: "old"}, nil
		})
	}()
	<-oldStarted
	cache.invalidate("weather")

	newStarted := make(chan struct{})
	newDone := make(chan InterfaceConfig, 1)
	go func() {
		value, err := cache.getOrLoad(context.Background(), time.Now(), "weather", func() (InterfaceConfig, error) {
			close(newStarted)
			return InterfaceConfig{APICode: "weather", Name: "new"}, nil
		})
		if err != nil {
			t.Errorf("fresh generation load failed: %v", err)
		}
		newDone <- value
	}()
	<-newStarted
	close(releaseOld)
	<-oldDone
	select {
	case got := <-newDone:
		if got.Name != "new" {
			t.Fatalf("fresh generation returned stale value: %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("fresh generation remained blocked by stale in-flight load")
	}
}
