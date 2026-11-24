package app

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCheckReadiness(t *testing.T) {
	ok := func(context.Context) error { return nil }
	if err := checkReadiness(context.Background(), ok, ok); err != nil {
		t.Fatalf("healthy dependencies returned error: %v", err)
	}
	redisCalled := false
	err := checkReadiness(context.Background(), func(context.Context) error { return errors.New("down") }, func(context.Context) error {
		redisCalled = true
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "mysql") || redisCalled {
		t.Fatalf("mysql failure = %v, redisCalled=%v", err, redisCalled)
	}
	err = checkReadiness(context.Background(), ok, func(context.Context) error { return errors.New("down") })
	if err == nil || !strings.Contains(err.Error(), "redis") {
		t.Fatalf("redis failure = %v", err)
	}
}

func TestReadinessProbeCachesSuccessAndFailure(t *testing.T) {
	now := time.Unix(1000, 0)
	var mysqlCalls atomic.Int32
	var redisCalls atomic.Int32
	failRedis := atomic.Bool{}
	probe := newReadinessProbe(
		func(context.Context) error {
			mysqlCalls.Add(1)
			return nil
		},
		func(context.Context) error {
			redisCalls.Add(1)
			if failRedis.Load() {
				return errors.New("redis down")
			}
			return nil
		},
	)
	probe.now = func() time.Time { return now }

	if err := probe.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := probe.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if mysqlCalls.Load() != 1 || redisCalls.Load() != 1 {
		t.Fatalf("successful readiness was not cached: mysql=%d redis=%d", mysqlCalls.Load(), redisCalls.Load())
	}

	now = now.Add(2 * time.Second)
	failRedis.Store(true)
	if err := probe.Check(context.Background()); err == nil {
		t.Fatal("expected Redis readiness failure")
	}
	failRedis.Store(false)
	if err := probe.Check(context.Background()); err == nil {
		t.Fatal("readiness failure was not cached")
	}
	if mysqlCalls.Load() != 2 || redisCalls.Load() != 2 {
		t.Fatalf("failed readiness cache called dependencies again: mysql=%d redis=%d", mysqlCalls.Load(), redisCalls.Load())
	}

	now = now.Add(2 * time.Second)
	if err := probe.Check(context.Background()); err != nil {
		t.Fatalf("readiness did not recover after failure cache expiry: %v", err)
	}
}

func TestReadinessProbeCoalescesConcurrentChecks(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var mysqlCalls atomic.Int32
	probe := newReadinessProbe(
		func(context.Context) error {
			if mysqlCalls.Add(1) == 1 {
				close(started)
			}
			<-release
			return nil
		},
		func(context.Context) error { return nil },
	)
	probe.timeout = 5 * time.Second

	const callers = 20
	start := make(chan struct{})
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
			errorsCh <- probe.Check(context.Background())
		}()
	}
	ready.Wait()
	close(start)
	<-started
	close(release)
	done.Wait()
	close(errorsCh)
	for err := range errorsCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	if mysqlCalls.Load() != 1 {
		t.Fatalf("concurrent readiness executed %d dependency checks, want 1", mysqlCalls.Load())
	}
}

func TestReadinessProbeWaiterCancellationDoesNotCancelLeader(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	probe := newReadinessProbe(
		func(context.Context) error {
			close(started)
			<-release
			return nil
		},
		func(context.Context) error { return nil },
	)
	probe.timeout = 5 * time.Second
	leaderDone := make(chan error, 1)
	go func() { leaderDone <- probe.Check(context.Background()) }()
	<-started

	waiterCtx, cancel := context.WithCancel(context.Background())
	waiterDone := make(chan error, 1)
	go func() { waiterDone <- probe.Check(waiterCtx) }()
	cancel()
	if err := <-waiterDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled waiter error=%v, want context.Canceled", err)
	}
	close(release)
	if err := <-leaderDone; err != nil {
		t.Fatalf("leader was cancelled by waiter: %v", err)
	}
}
