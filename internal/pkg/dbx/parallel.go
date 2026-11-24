// Package dbx provides small helpers for database access patterns shared
// across domain repositories.
package dbx

import (
	"context"
	"sync"
)

// RunParallel executes each function in its own goroutine and waits for all of
// them. It returns the first non-nil error, or nil if every function succeeds.
//
// Each function must capture its result into a variable it alone owns; two
// functions must never write the same variable. The caller combines the
// captured results after RunParallel returns.
//
// A panic in any function is re-raised in the caller's goroutine after every
// goroutine has finished, so upstream panic recovery (for example a singleflight
// loader that serves stale data on a panic) keeps working exactly as if the
// work had run synchronously.
func RunParallel(fns ...func() error) error {
	if len(fns) == 0 {
		return nil
	}
	var wg sync.WaitGroup
	errCh := make(chan error, len(fns))
	var (
		panicOnce sync.Once
		panicVal  any
	)
	for _, fn := range fns {
		wg.Add(1)
		go func(fn func() error) {
			defer wg.Done()
			defer func() {
				if p := recover(); p != nil {
					panicOnce.Do(func() { panicVal = p })
				}
			}()
			if err := fn(); err != nil {
				errCh <- err
			}
		}(fn)
	}
	wg.Wait()
	close(errCh)
	if panicVal != nil {
		panic(panicVal)
	}
	for err := range errCh {
		if err != nil {
			return err
		}
	}
	return nil
}

// RunParallelContext cancels sibling queries on the first failure, then waits
// for all callbacks to finish before returning their caller-owned results.
// Preserve the initiating error, not the context cancellation it causes.
func RunParallelContext(ctx context.Context, fns ...func(context.Context) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	var once sync.Once
	var firstErr error
	var panicOnce sync.Once
	var panicValue any
	for _, fn := range fns {
		wg.Add(1)
		go func(fn func(context.Context) error) {
			defer wg.Done()
			defer func() {
				if p := recover(); p != nil {
					panicOnce.Do(func() { panicValue = p })
					cancel()
				}
			}()
			if err := fn(ctx); err != nil {
				once.Do(func() { firstErr = err; cancel() })
			}
		}(fn)
	}
	wg.Wait()
	if panicValue != nil {
		panic(panicValue)
	}
	if firstErr != nil {
		return firstErr
	}
	return ctx.Err()
}
