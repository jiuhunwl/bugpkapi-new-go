package dbx

import (
	"errors"
	"testing"
)

func TestRunParallelReturnsFirstError(t *testing.T) {
	err := RunParallel(
		func() error { return nil },
		func() error { return errors.New("boom") },
	)
	if err == nil || err.Error() != "boom" {
		t.Fatalf("err = %v, want boom", err)
	}
}

func TestRunParallelSucceeds(t *testing.T) {
	if err := RunParallel(func() error { return nil }, func() error { return nil }); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunParallelRepanicsInCallerGoroutine(t *testing.T) {
	defer func() {
		if p := recover(); p == nil {
			t.Fatal("expected panic to propagate to the caller goroutine")
		}
	}()
	_ = RunParallel(
		func() error { return nil },
		func() error { panic("boom") },
	)
	t.Fatal("RunParallel must not return when a function panics")
}

func TestRunParallelEmpty(t *testing.T) {
	if err := RunParallel(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
