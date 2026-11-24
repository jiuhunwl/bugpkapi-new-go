package dbx

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRunParallelContextCancelsSiblingAndPreservesCause(t *testing.T) {
	cause := errors.New("count failed")
	started := make(chan struct{})
	stopped := make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := RunParallelContext(ctx,
		func(context.Context) error { <-started; return cause },
		func(ctx context.Context) error { close(started); <-ctx.Done(); close(stopped); return ctx.Err() },
	)
	if !errors.Is(err, cause) {
		t.Fatalf("got %v, want original query failure", err)
	}
	select {
	case <-stopped:
	default:
		t.Fatal("returned before sibling stopped")
	}
	if ctx.Err() != nil {
		t.Fatal("waited for parent deadline instead of canceling sibling")
	}
}

func TestRunParallelContextCancellationAndPanic(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	if err := RunParallelContext(ctx, func(context.Context) error { called = true; return nil }); !errors.Is(err, context.Canceled) || called {
		t.Fatalf("canceled work executed: %v, called=%v", err, called)
	}
	stopped := make(chan struct{})
	defer func() {
		if recover() != "boom" {
			t.Error("panic was not preserved")
		}
		select {
		case <-stopped:
		default:
			t.Error("panic left sibling running")
		}
	}()
	_ = RunParallelContext(context.Background(),
		func(context.Context) error { panic("boom") },
		func(ctx context.Context) error { <-ctx.Done(); close(stopped); return ctx.Err() },
	)
}
