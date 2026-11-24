package main

import (
	"context"
	"errors"
	"go.uber.org/zap"
	"testing"
	"time"
)

type shutdownFixture struct {
	release chan struct{}
	entered chan struct{}
	calls   int
}

func (f *shutdownFixture) Close() error { f.calls++; return context.DeadlineExceeded }
func (f *shutdownFixture) WaitClosed(ctx context.Context) error {
	close(f.entered)
	select {
	case <-f.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func TestCommandWaitsForFinalCleanupAfterInitialBudgetTimeout(t *testing.T) {
	fixture := &shutdownFixture{release: make(chan struct{}), entered: make(chan struct{})}
	result := make(chan error, 1)
	go func() { result <- closeApplication(fixture, zap.NewNop()) }()
	select {
	case err := <-result:
		close(fixture.release)
		t.Fatalf("command returned before final cleanup: %v", err)
	case <-fixture.entered:
	case <-time.After(time.Second):
		close(fixture.release)
		t.Fatal("command never waited for final cleanup")
	}
	close(fixture.release)
	select {
	case err := <-result:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("initial timeout was hidden: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("command did not finish after cleanup")
	}
	if fixture.calls != 1 {
		t.Fatalf("cleanup was restarted: %d", fixture.calls)
	}
}
