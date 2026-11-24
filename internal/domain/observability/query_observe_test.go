package observability

import (
	"context"
	"testing"
	"time"
)

func TestRunQueryReportsSlowQueries(t *testing.T) {
	repo := NewRepository(nil)
	var observed bool
	repo.slowQuery = func(name string, took time.Duration) {
		observed = name == "call_log_list" && took >= time.Second
	}
	err := repo.runQuery(context.Background(), "call_log_list", func(ctx context.Context) error {
		time.Sleep(1100 * time.Millisecond)
		return nil
	})
	if err != nil {
		t.Fatalf("runQuery returned error: %v", err)
	}
	if !observed {
		t.Fatalf("slow query was not reported")
	}
}

func TestRunQueryAppliesTimeout(t *testing.T) {
	repo := NewRepository(nil)
	repo.SetQueryTimeout(50 * time.Millisecond)
	err := repo.runQuery(context.Background(), "call_log_list", func(ctx context.Context) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
			return nil
		}
	})
	if err == nil {
		t.Fatalf("expected context deadline error")
	}
}
