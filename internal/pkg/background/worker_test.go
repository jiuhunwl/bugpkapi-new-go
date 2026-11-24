package background

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCancelledParentDoesNotRunTask(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var calls atomic.Int32
	worker := Start(ctx, func(context.Context) { calls.Add(1) })
	if err := worker.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatal("cancelled task ran")
	}
}

func TestCloseCancelsAndWaitsForTask(t *testing.T) {
	started, exited := make(chan struct{}), make(chan struct{})
	worker := Start(nil, func(ctx context.Context) { close(started); <-ctx.Done(); close(exited) })
	<-started
	var callers sync.WaitGroup
	for range 12 {
		callers.Go(func() {
			if err := worker.Close(context.Background()); err != nil {
				t.Errorf("Close: %v", err)
			}
		})
	}
	callers.Wait()
	select {
	case <-exited:
	default:
		t.Fatal("Close returned before task exit")
	}
}

func TestCloseTimeoutDoesNotPretendTaskStoppedAndAllowsRetry(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	worker := Start(context.Background(), func(context.Context) { close(started); <-release })
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := worker.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		close(release)
		t.Fatalf("Close must report unfinished work, got %v", err)
	}
	close(release)
	if err := worker.Close(nil); err != nil {
		t.Fatal(err)
	}
	if err := worker.Close(ctx); err != nil {
		t.Fatalf("completed work must win over an expired wait context: %v", err)
	}
}

func TestEmptyWorkerCloseIsSafe(t *testing.T) {
	var empty *Worker
	if err := empty.Close(nil); err != nil {
		t.Fatal(err)
	}
	if err := new(Worker).Close(nil); err != nil {
		t.Fatal(err)
	}
}
