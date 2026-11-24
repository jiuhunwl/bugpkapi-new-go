package ticket

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type ticketParallelDriver struct {
	state *ticketParallelState
}

type ticketParallelConn struct {
	state *ticketParallelState
}

type ticketParallelState struct {
	mode    string
	mu      sync.Mutex
	started map[string]bool
	ready   chan struct{}
}

type ticketParallelRows struct {
	columns []string
	values  [][]driver.Value
	offset  int
}

var ticketParallelDriverID atomic.Uint64

func (d *ticketParallelDriver) Open(string) (driver.Conn, error) {
	return &ticketParallelConn{state: d.state}, nil
}

func (c *ticketParallelConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare is not supported")
}

func (c *ticketParallelConn) Close() error { return nil }

func (c *ticketParallelConn) Begin() (driver.Tx, error) {
	return nil, errors.New("transactions are not supported")
}

func (c *ticketParallelConn) QueryContext(ctx context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	key, rows, err := c.state.classify(query)
	if err != nil {
		return nil, err
	}
	c.state.markStarted(key)
	select {
	case <-c.state.ready:
		return rows, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(300 * time.Millisecond):
		return nil, fmt.Errorf("query %s did not overlap its peers", key)
	}
}

func (s *ticketParallelState) classify(query string) (string, driver.Rows, error) {
	lower := strings.ToLower(query)
	if s.mode == "list" {
		switch {
		case strings.Contains(lower, ") filtered on filtered.id=t.id"):
			return "list", &ticketParallelRows{columns: make([]string, 27)}, nil
		case strings.Contains(lower, "coalesce(sum(status='open'),0)"):
			return "summary", &ticketParallelRows{
				columns: []string{"all", "open", "processing", "waiting", "resolved", "closed", "unread"},
				values:  [][]driver.Value{{int64(0), int64(0), int64(0), int64(0), int64(0), int64(0), int64(0)}},
			}, nil
		}
	}
	if s.mode == "statistics" {
		switch {
		case strings.Contains(lower, "avg(case when first_responded_at"):
			return "overview", &ticketParallelRows{
				columns: []string{"total", "created", "resolved", "first_overdue", "resolution_overdue", "avg_first", "avg_resolution"},
				values:  [][]driver.Value{{int64(0), int64(0), int64(0), int64(0), int64(0), nil, nil}},
			}, nil
		case strings.Contains(lower, "select status,count(*) from bp_ticket"):
			return "status", &ticketParallelRows{columns: []string{"status", "count"}}, nil
		case strings.Contains(lower, "select date_format(calendar.day"):
			return "daily", &ticketParallelRows{columns: []string{"day", "created", "resolved"}}, nil
		}
	}
	return "", nil, fmt.Errorf("unexpected %s query: %s", s.mode, query)
}

func (s *ticketParallelState) markStarted(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.started[key] = true
	want := 2
	if s.mode == "statistics" {
		want = 3
	}
	if len(s.started) == want {
		select {
		case <-s.ready:
		default:
			close(s.ready)
		}
	}
}

func (r *ticketParallelRows) Columns() []string { return r.columns }

func (r *ticketParallelRows) Close() error { return nil }

func (r *ticketParallelRows) Next(dest []driver.Value) error {
	if r.offset >= len(r.values) {
		return io.EOF
	}
	copy(dest, r.values[r.offset])
	r.offset++
	return nil
}

func openTicketParallelDB(t *testing.T, mode string) *sql.DB {
	t.Helper()
	state := &ticketParallelState{
		mode:    mode,
		ready:   make(chan struct{}),
		started: make(map[string]bool),
	}
	name := fmt.Sprintf("ticket-parallel-%d", ticketParallelDriverID.Add(1))
	sql.Register(name, &ticketParallelDriver{state: state})
	db, err := sql.Open(name, "")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(4)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestRepositoryListRunsPageAndSummaryConcurrently(t *testing.T) {
	repo := NewRepository(openTicketParallelDB(t, "list"))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	result, err := repo.List(ctx, 7, true, ListParams{Page: 1, PageSize: 20})
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 0 || len(result.Items) != 0 {
		t.Fatalf("unexpected list result: %+v", result)
	}
}

func TestRepositoryStatisticsRunsIndependentAggregatesConcurrently(t *testing.T) {
	repo := NewRepository(openTicketParallelDB(t, "statistics"))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	result, err := repo.Statistics(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	if result.RangeDays != 7 || len(result.Daily) != 0 || len(result.Status) != 0 {
		t.Fatalf("unexpected statistics result: %+v", result)
	}
}
