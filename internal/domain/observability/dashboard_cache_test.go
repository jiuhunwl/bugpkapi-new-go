package observability

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestDashboardReadCacheCoalescesConcurrentLoads(t *testing.T) {
	cache := newDashboardReadCache(time.Minute)
	const workers = 32
	var loads atomic.Int32
	start := make(chan struct{})
	var wait sync.WaitGroup
	results := make(chan int, workers)

	for index := 0; index < workers; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			value, err := cache.getOrLoad(context.Background(), time.Unix(100, 0), "overview", func() (any, error) {
				loads.Add(1)
				<-start
				return 42, nil
			})
			if err != nil {
				t.Errorf("dashboard cache load: %v", err)
				return
			}
			results <- value.(int)
		}()
	}

	for loads.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	close(start)
	wait.Wait()
	close(results)

	if loads.Load() != 1 {
		t.Fatalf("dashboard loads = %d, want 1", loads.Load())
	}
	for value := range results {
		if value != 42 {
			t.Fatalf("dashboard cached value = %d, want 42", value)
		}
	}
}

func TestDashboardReadCacheExpiresAndDoesNotCacheErrors(t *testing.T) {
	cache := newDashboardReadCache(time.Second)
	base := time.Unix(200, 0)
	loads := 0
	loader := func() (any, error) {
		loads++
		return loads, nil
	}

	first, err := cache.getOrLoad(context.Background(), base, "trend:7", loader)
	if err != nil || first.(int) != 1 {
		t.Fatalf("first dashboard read = %v, %v", first, err)
	}
	second, err := cache.getOrLoad(context.Background(), base.Add(500*time.Millisecond), "trend:7", loader)
	if err != nil || second.(int) != 1 || loads != 1 {
		t.Fatalf("fresh dashboard read = %v, %v, loads=%d", second, err, loads)
	}
	third, err := cache.getOrLoad(context.Background(), base.Add(2*time.Second), "trend:7", loader)
	if err != nil || third.(int) != 2 || loads != 2 {
		t.Fatalf("expired dashboard read = %v, %v, loads=%d", third, err, loads)
	}

	wantErr := errors.New("database unavailable")
	if _, err := cache.getOrLoad(context.Background(), base, "overview", func() (any, error) {
		return nil, wantErr
	}); !errors.Is(err, wantErr) {
		t.Fatalf("dashboard error = %v, want %v", err, wantErr)
	}
	value, err := cache.getOrLoad(context.Background(), base, "overview", func() (any, error) {
		return 9, nil
	})
	if err != nil || value.(int) != 9 {
		t.Fatalf("dashboard error was cached: %v, %v", value, err)
	}
}

func TestDashboardReadCacheLoaderPanicDoesNotLeaveKeyLoading(t *testing.T) {
	cache := newDashboardReadCache(time.Minute)
	var recovered any
	func() {
		defer func() { recovered = recover() }()
		_, _ = cache.getOrLoad(context.Background(), time.Unix(300, 0), "overview", func() (any, error) {
			panic("dashboard loader panic")
		})
	}()
	if recovered != "dashboard loader panic" {
		t.Fatalf("dashboard loader panic = %v, want original panic", recovered)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	value, err := cache.getOrLoad(ctx, time.Unix(301, 0), "overview", func() (any, error) {
		return 9, nil
	})
	if err != nil || value.(int) != 9 {
		t.Fatalf("dashboard cache remained stuck after loader panic: %v, %v", value, err)
	}
}
