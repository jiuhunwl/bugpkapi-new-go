// Package background owns cancellation and shutdown waiting for application tasks.
// Tasks remain responsible for their own deadlines, error handling and recovery.
package background

import (
	"context"
	"sync"
)

type Worker struct {
	cancel context.CancelFunc
	done   <-chan struct{}
	once   sync.Once
}

func Start(ctx context.Context, run func(context.Context)) *Worker {
	if ctx == nil {
		ctx = context.Background()
	}
	workerCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer cancel()
		if workerCtx.Err() == nil {
			run(workerCtx)
		}
	}()
	return &Worker{cancel: cancel, done: done}
}

// Close requests cancellation once and waits for actual task completion. A timed
// out caller can retry; cancellation alone is not treated as successful shutdown.
func (w *Worker) Close(ctx context.Context) error {
	if w == nil {
		return nil
	}
	w.once.Do(func() {
		if w.cancel != nil {
			w.cancel()
		}
	})
	if w.done == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-w.done:
		return nil
	default:
	}
	select {
	case <-w.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
