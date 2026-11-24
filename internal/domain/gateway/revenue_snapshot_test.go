package gateway

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"bugpk-api/internal/pkg/testdb"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

type revenueProjectFunc func(context.Context, int) (int, error)

func (f revenueProjectFunc) Process(ctx context.Context, limit int) (int, error) {
	return f(ctx, limit)
}

func TestRevenueSnapshotCloseReportsFinalProjectionFailure(t *testing.T) {
	expected := errors.New("projection unavailable")
	snapshot := NewRevenueSnapshot(revenueProjectFunc(func(context.Context, int) (int, error) { return 0, expected }))
	if err := snapshot.Close(); !errors.Is(err, expected) {
		t.Fatalf("close hid projection failure: %v", err)
	}
}
func TestRevenueSnapshotRejectsHintsAfterClose(t *testing.T) {
	snapshot := NewRevenueSnapshot(revenueProjectFunc(func(context.Context, int) (int, error) { return 0, nil }))
	if err := snapshot.Close(); err != nil {
		t.Fatal(err)
	}
	if snapshot.NotifyConfirmed() {
		t.Fatal("closed scheduler accepted a hint")
	}
}
func TestRevenueSnapshotProjectionHasBoundedContext(t *testing.T) {
	snapshot := NewRevenueSnapshot(revenueProjectFunc(func(ctx context.Context, _ int) (int, error) {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 3*time.Second {
			t.Error("unbounded projection context")
		}
		return 0, nil
	}))
	if err := snapshot.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
}
func TestRevenueSnapshotStartupFindsDurableWorkWithoutNotification(t *testing.T) {
	var pending atomic.Int64
	pending.Store(1)
	applied := make(chan struct{}, 1)
	snapshot := NewRevenueSnapshot(revenueProjectFunc(func(context.Context, int) (int, error) {
		n := pending.Swap(0)
		if n > 0 {
			applied <- struct{}{}
		}
		return int(n), nil
	})).Start(context.Background(), time.Hour)
	defer snapshot.Close()
	select {
	case <-applied:
	case <-time.After(time.Second):
		t.Fatal("lost wake-up prevented recovery of committed work")
	}
}
func TestRevenueSnapshotInterleavesStartCloseAndDurableAdmissions(t *testing.T) {
	var pending, projected atomic.Int64
	projector := revenueProjectFunc(func(context.Context, int) (int, error) { n := pending.Swap(0); projected.Add(n); return int(n), nil })
	snapshot := NewRevenueSnapshot(projector)
	start := make(chan struct{})
	var jobs sync.WaitGroup
	for range 24 {
		jobs.Go(func() { <-start; snapshot.Start(context.Background(), time.Hour) })
		jobs.Go(func() { <-start; pending.Add(1); snapshot.NotifyConfirmed() })
		jobs.Go(func() {
			<-start
			if err := snapshot.CloseContext(context.Background()); err != nil {
				t.Error(err)
			}
		})
	}
	close(start)
	jobs.Wait()
	if snapshot.NotifyConfirmed() {
		t.Fatal("closed scheduler restarted")
	}
	// Work committed after shutdown is recovered by another process from storage,
	// not carried by the old scheduler's return value or memory.
	restarted := NewRevenueSnapshot(projector)
	if err := restarted.Close(); err != nil {
		t.Fatal(err)
	}
	if projected.Load() != 24 || pending.Load() != 0 {
		t.Fatalf("durable work lost: projected=%d pending=%d", projected.Load(), pending.Load())
	}
}
func TestRevenueSnapshotCloseDeadlineDoesNotEraseDurableWork(t *testing.T) {
	var pending atomic.Int64
	pending.Store(1)
	snapshot := NewRevenueSnapshot(revenueProjectFunc(func(ctx context.Context, _ int) (int, error) { <-ctx.Done(); return 0, ctx.Err() }))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := snapshot.CloseContext(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline hidden: %v", err)
	}
	if pending.Load() != 1 {
		t.Fatal("scheduler erased durable work")
	}
}
func TestRevenueSnapshotFlushQueueHonorsCancellation(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	snapshot := NewRevenueSnapshot(revenueProjectFunc(func(ctx context.Context, _ int) (int, error) {
		if calls.Add(1) == 1 {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return 0, ctx.Err()
			}
		}
		return 0, nil
	}))
	first := make(chan error, 1)
	go func() { first <- snapshot.Flush(context.Background()) }()
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := snapshot.Flush(ctx)
	close(release)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("queued projection ignored cancellation: %v", err)
	}
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal("cancelled queued call reached persistence")
	}
}
func TestRevenueSnapshotCloseDoesNotCancelRunningCommit(t *testing.T) {
	entered := make(chan context.Context, 1)
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	var calls atomic.Int32
	snapshot := NewRevenueSnapshot(revenueProjectFunc(func(ctx context.Context, _ int) (int, error) {
		if calls.Add(1) == 1 {
			entered <- ctx
			select {
			case <-release:
			case <-ctx.Done():
				return 0, ctx.Err()
			}
		}
		return 0, nil
	})).Start(context.Background(), time.Hour)
	writeCtx := <-entered
	closed := make(chan error, 1)
	go func() { closed <- snapshot.Close() }()
	deadline := time.Now().Add(time.Second)
	for {
		snapshot.mu.Lock()
		closing := snapshot.closing
		snapshot.mu.Unlock()
		if closing {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("close did not start")
		}
		time.Sleep(time.Millisecond)
	}
	if writeCtx.Err() != nil {
		t.Fatal("shutdown interrupted a running transaction")
	}
	unblock()
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
}
func TestRevenueFallbackFailureIsBoundedObservableAndDoesNotReverseBilling(t *testing.T) {
	var called bool
	db := testdb.OpenStub(t, testdb.SQLStub{Begin: func(ctx context.Context, _ driver.TxOptions) (driver.Tx, error) {
		called = true
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 3*time.Second || ctx.Err() != nil {
			t.Error("fallback context not independently bounded")
		}
		return nil, errors.New("projection failed password=private-secret")
	}})
	repo := NewRepository(db)
	core, logs := observer.New(zap.ErrorLevel)
	service := NewService(repo, nil, nil, "test-secret")
	service.SetLogger(zap.New(core))
	defer service.Close(context.Background())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	repo.notifyConfirmedRevenue(ctx, BillingReservation{BillingRecordID: 42, ChargeAmount: "must-not-be-used-as-delta"})
	entries := logs.FilterMessage("revenue snapshot fallback failed").All()
	if !called || len(entries) != 1 {
		t.Fatalf("fallback was not observable: called=%v logs=%d", called, len(entries))
	}
	fields := entries[0].ContextMap()
	if fields["billing_record_id"] != int64(42) || strings.Contains(fmt.Sprint(fields), "private-secret") {
		t.Fatalf("unsafe error context: %v", fields)
	}
}
