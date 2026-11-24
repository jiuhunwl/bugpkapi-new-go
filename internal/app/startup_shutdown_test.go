package app

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"bugpk-api/internal/config"
	"bugpk-api/internal/domain/gateway"
	"bugpk-api/internal/pkg/testdb"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

type lifecycleRevenueProjector func(context.Context, int) (int, error)

func (f lifecycleRevenueProjector) Process(ctx context.Context, limit int) (int, error) {
	return f(ctx, limit)
}

type lifecycleTx struct{}

func (lifecycleTx) Commit() error   { return nil }
func (lifecycleTx) Rollback() error { return nil }

func TestFailedApplicationCompositionStopsStartedWorkerBeforeDependencies(t *testing.T) {
	primary := errors.New("provider initialization failed")
	started := make(chan context.Context, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseQuery := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseQuery()
	var queryReturned, closedBeforeQuery atomic.Bool
	db := testdb.OpenStub(t, testdb.SQLStub{
		Begin: func(context.Context, driver.TxOptions) (driver.Tx, error) { return lifecycleTx{}, nil },
		Close: func() error {
			if !queryReturned.Load() {
				closedBeforeQuery.Store(true)
			}
			return nil
		},
		Query: func(ctx context.Context, q string, _ []driver.NamedValue) (driver.Rows, error) {
			if !strings.Contains(q, "bp_message_provider_config") {
				return nil, fmt.Errorf("unexpected startup query: %s", q)
			}
			select {
			case workerCtx := <-started:
				started <- workerCtx
				return nil, primary
			case <-time.After(time.Second):
				return nil, errors.New("analytics did not start")
			}
		},
		Exec: func(ctx context.Context, q string, _ []driver.NamedValue) (driver.Result, error) {
			if !strings.Contains(q, "bp_analytics_checkpoint") {
				return nil, fmt.Errorf("unexpected startup write: %s", q)
			}
			select {
			case started <- ctx:
			default:
			}
			defer queryReturned.Store(true)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-release:
				return nil, errors.New("test released query")
			}
		},
	})
	db.SetMaxOpenConns(2)
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", MaxRetries: -1})
	defer rdb.Close()
	cfg := config.Default()
	cfg.Analytics.Enabled = true
	cfg.Analytics.PollInterval = time.Hour
	cfg.Server.ShutdownTimeout = time.Second
	done := make(chan error, 1)
	go func() {
		app, err := newApplicationWithConnections(context.Background(), cfg, zap.NewNop(), db, rdb)
		if app != nil {
			_ = app.Close()
		}
		done <- err
	}()
	var err error
	select {
	case err = <-done:
	case <-time.After(1500 * time.Millisecond):
		releaseQuery()
		err = <-done
		t.Error("startup rollback failed to cancel its worker")
	}
	if !errors.Is(err, primary) {
		t.Fatalf("startup error lost: %v", err)
	}
	select {
	case workerCtx := <-started:
		if !errors.Is(workerCtx.Err(), context.Canceled) {
			t.Error("worker still has a live context after failed startup")
		}
	default:
		t.Error("worker never ran")
	}
	if closedBeforeQuery.Load() {
		t.Error("database closed before worker exit")
	}
	if err := db.PingContext(context.Background()); err == nil {
		t.Error("startup leaked database")
	}
	if err := rdb.Ping(context.Background()).Err(); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("startup leaked redis client: %v", err)
	}
}

func TestApplicationCloseDrainsProducerBeforeRevenueSink(t *testing.T) {
	var amounts []string
	db := testdb.OpenStub(t, testdb.SQLStub{Exec: func(_ context.Context, _ string, args []driver.NamedValue) (driver.Result, error) {
		amounts = append(amounts, args[0].Value.(string))
		return driver.RowsAffected(1), nil
	}})
	var pending atomic.Bool
	revenue := gateway.NewRevenueSnapshot(lifecycleRevenueProjector(func(ctx context.Context, _ int) (int, error) {
		if pending.Swap(false) {
			_, err := db.ExecContext(ctx, "test durable projection", "1.2500")
			return 1, err
		}
		return 0, nil
	})).Start(context.Background(), time.Hour)
	application := &Application{backgroundClosers: []func(context.Context) error{
		func(context.Context) error { return revenue.Close() },
		func(context.Context) error {
			pending.Store(true)
			if !revenue.NotifyConfirmed() {
				return errors.New("revenue sink closed before producer")
			}
			return nil
		},
	}}
	if err := application.Close(); err != nil {
		t.Fatal(err)
	}
	if len(amounts) != 1 || amounts[0] != "1.2500" {
		t.Fatalf("accepted shutdown revenue was not flushed: %v", amounts)
	}
}

func TestApplicationCloseUsesConfiguredBudget(t *testing.T) {
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	application := &Application{Config: config.Config{Server: config.ServerConfig{ShutdownTimeout: 30 * time.Millisecond}}}
	application.backgroundClosers = []func(context.Context) error{func(ctx context.Context) error {
		<-release
		if ctx.Err() != nil {
			t.Error("caller wait timeout poisoned ordered cleanup")
		}
		return nil
	}}
	before := time.Now()
	err := application.Close()
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(before) > 250*time.Millisecond {
		t.Fatalf("close did not bound caller wait: %v", err)
	}
	unblock()
	if err := application.Close(); err != nil {
		t.Fatalf("completed cleanup could not be awaited again: %v", err)
	}
}

func TestStartupCancellationReachesInitializationQueries(t *testing.T) {
	entered := make(chan struct{})
	db := testdb.OpenStub(t, testdb.SQLStub{Begin: func(context.Context, driver.TxOptions) (driver.Tx, error) { return lifecycleTx{}, nil }, Query: func(ctx context.Context, _ string, _ []driver.NamedValue) (driver.Rows, error) {
		close(entered)
		<-ctx.Done()
		return nil, ctx.Err()
	}})
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", MaxRetries: -1})
	defer rdb.Close()
	cfg := config.Default()
	cfg.Analytics.Enabled = false
	cfg.Server.ShutdownTimeout = time.Second
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := newApplicationWithConnections(ctx, cfg, zap.NewNop(), db, rdb); done <- err }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("initialization query did not run")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("startup cancellation lost: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("startup ignored cancellation")
	}
	if err := db.PingContext(context.Background()); err == nil {
		t.Fatal("cancelled startup leaked database")
	}
}
func TestStartupRollbackPreservesInitializationAndCleanupErrors(t *testing.T) {
	initErr, closeErr := errors.New("initialization failed"), errors.New("database close failed")
	db := testdb.OpenStub(t, testdb.SQLStub{Begin: func(context.Context, driver.TxOptions) (driver.Tx, error) { return lifecycleTx{}, nil }, Query: func(context.Context, string, []driver.NamedValue) (driver.Rows, error) { return nil, initErr }, Close: func() error { return closeErr }})
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", MaxRetries: -1})
	defer rdb.Close()
	cfg := config.Default()
	cfg.Analytics.Enabled = false
	application, err := newApplicationWithConnections(context.Background(), cfg, zap.NewNop(), db, rdb)
	if application != nil || !errors.Is(err, initErr) || !errors.Is(err, closeErr) {
		t.Fatalf("startup/cleanup errors not preserved: %v", err)
	}
}
func TestStartupPanicStillReleasesConnections(t *testing.T) {
	db := testdb.OpenStub(t, testdb.SQLStub{})
	if err := db.PingContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", MaxRetries: -1})
	defer rdb.Close()
	cfg := config.Default()
	cfg.Analytics.Enabled = false
	core, _ := observer.New(zap.WarnLevel)
	logger := zap.New(core, zap.Hooks(func(zapcore.Entry) error { panic("startup-panic") }))
	func() {
		defer func() {
			if recover() != "startup-panic" {
				t.Error("original startup panic changed")
			}
		}()
		_, _ = newApplicationWithConnections(context.Background(), cfg, logger, db, rdb)
	}()
	if db.PingContext(context.Background()) == nil {
		t.Fatal("panic leaked database")
	}
	if err := rdb.Ping(context.Background()).Err(); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatal("panic leaked Redis client")
	}
}

func TestSuccessfulCompositionTransfersConnectionOwnership(t *testing.T) {
	db := testdb.OpenStub(t, testdb.SQLStub{
		Begin: func(context.Context, driver.TxOptions) (driver.Tx, error) { return lifecycleTx{}, nil },
		Query: func(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
			if strings.Contains(query, "select id from bp_billing_revenue_projection") {
				return testdb.Rows([]driver.Value{int64(1)}), nil
			}
			return testdb.Rows(), nil
		},
		Exec: func(context.Context, string, []driver.NamedValue) (driver.Result, error) {
			return driver.RowsAffected(1), nil
		},
	})
	// Route composition may start the invalidation subscriber; no real Redis or
	// outside network is used by this lifecycle-only fixture.
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", MaxRetries: -1, Dialer: func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("test network disabled")
	}})
	cfg := config.Default()
	cfg.Analytics.Enabled = false
	cfg.Auth.Secret = strings.Repeat("s", 64)
	cfg.System.ConfigSecret = strings.Repeat("k", 32)
	cfg.Server.ShutdownTimeout = 2 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	application, err := newApplicationWithConnections(ctx, cfg, zap.NewNop(), db, rdb)
	if err != nil {
		t.Fatal(err)
	}
	defer application.Close()
	if application.DB != db || application.Redis != rdb || application.Server == nil {
		t.Fatal("successful composition did not transfer owned resources")
	}
	if err := db.PingContext(context.Background()); err != nil {
		t.Fatalf("successful startup closed database: %v", err)
	}
	if err := application.Close(); err != nil {
		t.Fatalf("normal shutdown failed: %v", err)
	}
	if db.PingContext(context.Background()) == nil {
		t.Fatal("shutdown left database open")
	}
}

func TestLateStartupPanicRecoversRegisteredServicesAndReportsCleanupError(t *testing.T) {
	closeErr := errors.New("late cleanup failure")
	db := testdb.OpenStub(t, testdb.SQLStub{
		Begin: func(context.Context, driver.TxOptions) (driver.Tx, error) { return lifecycleTx{}, nil },
		Query: func(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
			if strings.Contains(query, "select id from bp_billing_revenue_projection") {
				return testdb.Rows([]driver.Value{int64(1)}), nil
			}
			return testdb.Rows(), nil
		},
		Exec: func(context.Context, string, []driver.NamedValue) (driver.Result, error) {
			return driver.RowsAffected(1), nil
		},
		Close: func() error { return closeErr },
	})
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", MaxRetries: -1, Dialer: func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("test network disabled")
	}})
	defer rdb.Close()
	cfg := config.Default()
	cfg.Analytics.Enabled = false
	cfg.Auth.Secret = strings.Repeat("s", 64)
	cfg.System.ConfigSecret = strings.Repeat("k", 32)
	cfg.Server.ShutdownTimeout = 2 * time.Second
	core, logs := observer.New(zap.DebugLevel)
	logger := zap.New(core, zap.Hooks(func(entry zapcore.Entry) error {
		if entry.Message == "home overview analytics snapshot prime failed" {
			panic("late-startup-panic")
		}
		return nil
	}))
	func() {
		defer func() {
			if recover() != "late-startup-panic" {
				t.Error("late startup panic lost")
			}
		}()
		_, _ = newApplicationWithConnections(context.Background(), cfg, logger, db, rdb)
	}()
	if db.PingContext(context.Background()) == nil {
		t.Fatal("late panic leaked database")
	}
	if err := rdb.Ping(context.Background()).Err(); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatal("late panic leaked redis")
	}
	if logs.FilterMessage("startup cleanup failed").Len() != 1 {
		t.Fatal("cleanup error accompanying panic was not reported")
	}
}

func TestFailedStartupWaitsForFinalCleanupEvenAfterInitialBudget(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	primary := errors.New("startup failure before resources are returned")
	db := testdb.OpenStub(t, testdb.SQLStub{Begin: func(context.Context, driver.TxOptions) (driver.Tx, error) { return lifecycleTx{}, nil }, Query: func(context.Context, string, []driver.NamedValue) (driver.Rows, error) { return nil, primary }, Close: func() error { close(entered); <-release; return nil }})
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", MaxRetries: -1})
	defer rdb.Close()
	cfg := config.Default()
	cfg.Analytics.Enabled = false
	cfg.Server.ShutdownTimeout = 20 * time.Millisecond
	returned := make(chan error, 1)
	go func() {
		_, err := newApplicationWithConnections(context.Background(), cfg, zap.NewNop(), db, rdb)
		returned <- err
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("cleanup did not start")
	}
	select {
	case err := <-returned:
		t.Fatalf("failed constructor abandoned cleanup: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	unblock()
	select {
	case err := <-returned:
		if !errors.Is(err, primary) || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("startup/initial wait errors lost: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("failed startup did not finish after cleanup")
	}
}
