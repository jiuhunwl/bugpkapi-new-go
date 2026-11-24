package observability

import (
	apperrors "bugpk-api/internal/pkg/errors"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCallLogFastPaginationIsExplicitAndBounded(t *testing.T) {
	p, err := callLogParamsForQuery("page=1")
	if err != nil || p.SkipTotal {
		t.Fatalf("default exact-count contract changed: %+v %v", p, err)
	}
	p, err = callLogParamsForQuery("includeTotal=false&startTime=2026-09-01%2000:00:00&endTime=2026-09-20%2000:00:00")
	if err != nil || !p.SkipTotal {
		t.Fatalf("fast pagination not enabled: %+v %v", p, err)
	}
	for _, query := range []string{
		"includeTotal=wrong", "includeTotal=false", "includeTotal=false&startTime=2026-09-01%2000:00:00",
		"includeTotal=false&startTime=2026-01-01%2000:00:00&endTime=2026-09-20%2000:00:00",
		"includeTotal=false&startTime=2026-09-01%2000:00:00&endTime=2026-09-20%2000:00:00&page=2",
	} {
		if _, err := callLogParamsForQuery(query); err == nil {
			t.Fatalf("unbounded fast query accepted: %s", query)
		}
	}
}

func TestUnknownTotalSurvivesUserResponseMapping(t *testing.T) {
	result := userCallLogPage(PageResult[CallLogItem]{Items: []CallLogItem{}, Total: -1, TotalOmitted: true, NextCursor: "next"})
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range []string{`"total":-1`, `"totalOmitted":true`, `"nextCursor":"next"`} {
		if !strings.Contains(string(encoded), part) {
			t.Fatalf("unknown count metadata lost: %s", encoded)
		}
	}
}

func TestRepositorySkipsCountOnlyWhenExplicitlyRequested(t *testing.T) {
	for _, skip := range []bool{false, true} {
		connector := &fastPageConnector{}
		db := sql.OpenDB(connector)
		repo := NewRepository(db)
		result, err := repo.ListCallLogs(context.Background(), CallLogListParams{SkipTotal: skip, StartTime: "2026-09-01 00:00:00", EndTime: "2026-09-20 00:00:00"}, true, 1)
		db.Close()
		if err != nil {
			t.Fatal(err)
		}
		if skip {
			if connector.counts.Load() != 0 || result.Total != -1 || !result.TotalOmitted {
				t.Fatalf("fast query performed count or invented total: %+v count=%d", result, connector.counts.Load())
			}
		} else if connector.counts.Load() != 1 || result.Total != 42 || result.TotalOmitted {
			t.Fatalf("legacy exact total changed: %+v", result)
		}
	}
}

type fastPageConnector struct {
	counts atomic.Int64
	query  func(context.Context, string) error
}

func (c *fastPageConnector) Connect(context.Context) (driver.Conn, error) {
	return &fastPageConn{owner: c}, nil
}
func (c *fastPageConnector) Driver() driver.Driver { return fastPageDriver{} }

type fastPageDriver struct{}

func (fastPageDriver) Open(string) (driver.Conn, error) { return nil, errors.New("use connector") }

type fastPageConn struct{ owner *fastPageConnector }

func (*fastPageConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected prepare")
}
func (*fastPageConn) Begin() (driver.Tx, error) { return nil, errors.New("unexpected transaction") }
func (*fastPageConn) Close() error              { return nil }
func (c *fastPageConn) QueryContext(ctx context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	if c.owner.query != nil {
		if err := c.owner.query(ctx, query); err != nil {
			return nil, err
		}
	}
	if strings.Contains(query, "select count(*)") {
		c.owner.counts.Add(1)
		return &fastPageRows{count: true}, nil
	}
	return &fastPageRows{}, nil
}

type fastPageRows struct {
	count bool
	done  bool
}

func (*fastPageRows) Columns() []string { return []string{"value"} }
func (*fastPageRows) Close() error      { return nil }
func (r *fastPageRows) Next(values []driver.Value) error {
	if !r.count || r.done {
		return io.EOF
	}
	values[0] = int64(42)
	r.done = true
	return nil
}

func TestCallLogListFailureCancelsCount(t *testing.T) {
	started := make(chan struct{})
	stopped := make(chan struct{})
	cause := errors.New("list query failed")
	connector := &fastPageConnector{query: func(ctx context.Context, query string) error {
		if strings.Contains(query, "select count(*)") {
			close(started)
			<-ctx.Done()
			close(stopped)
			return ctx.Err()
		}
		<-started
		return cause
	}}
	db := sql.OpenDB(connector)
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := NewRepository(db).ListCallLogs(ctx, CallLogListParams{}, true, 1)
	if !errors.Is(err, cause) {
		t.Fatalf("wrong failure: %v", err)
	}
	if ctx.Err() != nil {
		t.Fatal("failed list left count running until request deadline")
	}
	select {
	case <-stopped:
	default:
		t.Fatal("count still running after return")
	}
}

func TestCallLogHeavyReadsLeavePoolCapacityAndRejectExcess(t *testing.T) {
	started := make(chan struct{}, 8)
	release := make(chan struct{})
	connector := &fastPageConnector{query: func(ctx context.Context, _ string) error {
		started <- struct{}{}
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(8)
	defer db.Close()
	repo := NewRepository(db)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	defer close(release)
	done := make(chan error, 2)
	for range 2 {
		go func() { _, err := repo.ListCallLogs(ctx, CallLogListParams{SkipTotal: true}, true, 1); done <- err }()
	}
	for range 2 {
		select {
		case <-started:
		case <-ctx.Done():
			t.Fatal("initial reads did not start")
		}
	}
	before := time.Now()
	_, err := repo.ListCallLogs(ctx, CallLogListParams{SkipTotal: true}, true, 1)
	business, ok := apperrors.AsBusinessError(err)
	if !ok || business.StatusCode != 429 {
		t.Errorf("expected bounded overload rejection, got %v", err)
	}
	if time.Since(before) > time.Second {
		t.Error("overloaded query waited for the full DB timeout")
	}
	if len(started) != 0 {
		t.Error("overloaded query reached the database")
	}
	cancel()
	for range 2 {
		<-done
	}
	// Cancellation must release the read slots, including when database queries fail.
	connector.query = nil
	if _, err := repo.ListCallLogs(context.Background(), CallLogListParams{SkipTotal: true}, true, 1); err != nil {
		t.Errorf("read slot leaked: %v", err)
	}
}

func TestCallLogListBudgetsAndConcurrentSuccess(t *testing.T) {
	for _, skip := range []bool{false, true} {
		t.Run(fmt.Sprint(skip), func(t *testing.T) {
			entered := make(chan struct{}, 2)
			release := make(chan struct{})
			connector := &fastPageConnector{query: func(ctx context.Context, _ string) error {
				deadline, ok := ctx.Deadline()
				limit := 15 * time.Second
				if skip {
					limit = 6 * time.Second
				}
				if !ok || time.Until(deadline) > limit || time.Until(deadline) < limit-time.Second {
					t.Errorf("wrong request budget: %s", time.Until(deadline))
				}
				entered <- struct{}{}
				select {
				case <-release:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			}}
			db := sql.OpenDB(connector)
			defer db.Close()
			repo := NewRepository(db)
			done := make(chan error, 1)
			go func() {
				_, err := repo.ListCallLogs(context.Background(), CallLogListParams{SkipTotal: skip}, true, 1)
				done <- err
			}()
			queries := 2
			if skip {
				queries = 1
			}
			timer := time.NewTimer(time.Second)
			defer timer.Stop()
			for range queries {
				select {
				case <-entered:
				case <-timer.C:
					close(release)
					<-done
					t.Fatal("queries did not run concurrently")
				}
			}
			close(release)
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCallLogConcurrentBurstIsBounded(t *testing.T) {
	const clients = 128
	const admitted = 8
	release := make(chan struct{})
	var started atomic.Int64
	connector := &fastPageConnector{query: func(ctx context.Context, _ string) error {
		started.Add(1)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(100)
	defer db.Close()
	repo := NewRepository(db)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, clients)
	for range clients {
		go func() { _, err := repo.ListCallLogs(ctx, CallLogListParams{SkipTotal: true}, true, 1); done <- err }()
	}
	for range clients - admitted {
		err := <-done
		business, ok := apperrors.AsBusinessError(err)
		if !ok || business.StatusCode != 429 {
			t.Errorf("unexpected overflow result: %v", err)
		}
	}
	if n := started.Load(); n != admitted {
		t.Errorf("burst reached %d DB queries, want %d", n, admitted)
	}
	close(release)
	for range admitted {
		if err := <-done; err != nil {
			t.Errorf("admitted query failed: %v", err)
		}
	}
	if db.Stats().InUse != 0 {
		t.Error("burst leaked database connections")
	}
	t.Logf("clients=%d admitted=%d rejected=%d db_in_use_after=%d", clients, admitted, clients-admitted, db.Stats().InUse)
}
