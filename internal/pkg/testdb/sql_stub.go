package testdb

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"io"
	"sync/atomic"
)

// SQLStub runs the real database/sql machinery without network or external data.
// Missing callbacks fail explicitly so unexpected queries never silently succeed.
type SQLStub struct {
	Begin func(context.Context, driver.TxOptions) (driver.Tx, error)
	Close func() error
	Query func(context.Context, string, []driver.NamedValue) (driver.Rows, error)
	Exec  func(context.Context, string, []driver.NamedValue) (driver.Result, error)
}
type stubTest interface {
	Helper()
	Cleanup(func())
	Fatalf(string, ...any)
}
type stubDriver struct{ callbacks SQLStub }
type stubConn struct{ callbacks SQLStub }

var stubSequence atomic.Uint64

func OpenStub(t stubTest, callbacks SQLStub) *sql.DB {
	t.Helper()
	name := fmt.Sprintf("bugpk-sql-stub-%d", stubSequence.Add(1))
	sql.Register(name, stubDriver{callbacks})
	db, err := sql.Open(name, "")
	if err != nil {
		t.Fatalf("open SQL stub: %v", err)
		return nil
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}
func (d stubDriver) Open(string) (driver.Conn, error) { return &stubConn{d.callbacks}, nil }
func (c *stubConn) Close() error {
	if c.callbacks.Close != nil {
		return c.callbacks.Close()
	}
	return nil
}
func (*stubConn) Begin() (driver.Tx, error) { return nil, fmt.Errorf("unexpected SQL transaction") }
func (c *stubConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if c.callbacks.Begin == nil {
		return nil, fmt.Errorf("unexpected SQL transaction")
	}
	return c.callbacks.Begin(ctx, opts)
}
func (*stubConn) Prepare(string) (driver.Stmt, error) {
	return nil, fmt.Errorf("unexpected prepared statement")
}
func (c *stubConn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	if c.callbacks.Query == nil {
		return nil, fmt.Errorf("unexpected SQL query: %s", q)
	}
	return c.callbacks.Query(ctx, q, args)
}
func (c *stubConn) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	if c.callbacks.Exec == nil {
		return nil, fmt.Errorf("unexpected SQL write: %s", q)
	}
	return c.callbacks.Exec(ctx, q, args)
}

type stubRows struct {
	rows         [][]driver.Value
	width, index int
}

func Rows(values ...[]driver.Value) driver.Rows {
	width := 0
	if len(values) > 0 {
		width = len(values[0])
	}
	return &stubRows{rows: values, width: width}
}
func (r *stubRows) Columns() []string {
	columns := make([]string, r.width)
	for i := range columns {
		columns[i] = fmt.Sprint(i)
	}
	return columns
}
func (*stubRows) Close() error { return nil }
func (r *stubRows) Next(dest []driver.Value) error {
	if r.index >= len(r.rows) {
		return io.EOF
	}
	if len(r.rows[r.index]) != r.width {
		return fmt.Errorf("inconsistent SQL stub row width")
	}
	copy(dest, r.rows[r.index])
	r.index++
	return nil
}

// ReadOnlyTransaction is an explicit fixture for repositories that require a
// consistent read view. Unexpected writable or weaker transactions fail tests.
func ReadOnlyTransaction(_ context.Context, options driver.TxOptions) (driver.Tx, error) {
	if !options.ReadOnly || options.Isolation != driver.IsolationLevel(sql.LevelRepeatableRead) {
		return nil, fmt.Errorf("expected read-only repeatable-read transaction")
	}
	return readOnlyTransaction{}, nil
}

type readOnlyTransaction struct{}

func (readOnlyTransaction) Commit() error   { return nil }
func (readOnlyTransaction) Rollback() error { return nil }
