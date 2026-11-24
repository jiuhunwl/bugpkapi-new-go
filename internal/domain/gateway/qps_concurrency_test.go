package gateway

import (
	"context"
	"testing"
)

func TestLoadEffectiveQPSRunsIndependentLookupsConcurrently(t *testing.T) {
	roleStarted := make(chan struct{})
	packageStarted := make(chan struct{})
	release := make(chan struct{})
	resultCh := make(chan struct {
		role         int
		packageLimit int
		found        bool
		err          error
	}, 1)

	go func() {
		role, packageLimit, found, err := loadEffectiveQPS(
			context.Background(),
			func(context.Context) (int, error) {
				close(roleStarted)
				<-release
				return 25, nil
			},
			func(context.Context) (int, bool, error) {
				close(packageStarted)
				<-release
				return 80, true, nil
			},
		)
		resultCh <- struct {
			role         int
			packageLimit int
			found        bool
			err          error
		}{role, packageLimit, found, err}
	}()

	<-roleStarted
	<-packageStarted
	close(release)
	result := <-resultCh
	if result.err != nil {
		t.Fatalf("loadEffectiveQPS() error = %v", result.err)
	}
	if result.role != 25 || result.packageLimit != 80 || !result.found {
		t.Fatalf("loadEffectiveQPS() = %+v", result)
	}
}
