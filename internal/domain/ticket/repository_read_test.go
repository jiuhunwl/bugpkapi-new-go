package ticket

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"testing"
)

type ticketReadDriverState struct {
	affected int64
	execArgs []driver.NamedValue
	execSQL  string
	exists   int64
}

type ticketReadDriver struct{ state *ticketReadDriverState }
type ticketReadConn struct{ state *ticketReadDriverState }
type ticketReadRows struct {
	done   bool
	exists int64
}

var ticketReadDriverID atomic.Uint64

func (d *ticketReadDriver) Open(string) (driver.Conn, error) {
	return &ticketReadConn{state: d.state}, nil
}

func (c *ticketReadConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare is not supported")
}
func (c *ticketReadConn) Close() error { return nil }
func (c *ticketReadConn) Begin() (driver.Tx, error) {
	return nil, errors.New("transactions are not supported")
}
func (c *ticketReadConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.state.execSQL = query
	c.state.execArgs = append([]driver.NamedValue(nil), args...)
	return driver.RowsAffected(c.state.affected), nil
}
func (c *ticketReadConn) QueryContext(_ context.Context, _ string, _ []driver.NamedValue) (driver.Rows, error) {
	return &ticketReadRows{exists: c.state.exists}, nil
}

func (r *ticketReadRows) Columns() []string { return []string{"exists"} }
func (r *ticketReadRows) Close() error      { return nil }
func (r *ticketReadRows) Next(dest []driver.Value) error {
	if r.done {
		return io.EOF
	}
	r.done = true
	dest[0] = r.exists
	return nil
}

func openTicketReadDB(t *testing.T, state *ticketReadDriverState) *sql.DB {
	t.Helper()
	name := fmt.Sprintf("ticket-read-test-%d", ticketReadDriverID.Add(1))
	sql.Register(name, &ticketReadDriver{state: state})
	db, err := sql.Open(name, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestRepositoryMarkReadUsesTheMessageSnapshot(t *testing.T) {
	state := &ticketReadDriverState{affected: 1}
	repo := NewRepository(openTicketReadDB(t, state))

	updated, err := repo.MarkRead(context.Background(), 12, 7, false, "2026-08-15 10:01:02", 4)
	if err != nil || !updated {
		t.Fatalf("MarkRead() = %v, %v; want true", updated, err)
	}
	for _, fragment := range []string{"unread_user>0", "last_message_at=?", "message_count=?", "user_id=?"} {
		if !strings.Contains(state.execSQL, fragment) {
			t.Fatalf("read update is missing %q: %s", fragment, state.execSQL)
		}
	}
	wantArgs := []any{int64(12), int64(7), "2026-08-15 10:01:02", int64(4)}
	if len(state.execArgs) != len(wantArgs) {
		t.Fatalf("read update args = %#v", state.execArgs)
	}
	for index, want := range wantArgs {
		if state.execArgs[index].Value != want {
			t.Fatalf("read update arg %d = %#v, want %#v", index, state.execArgs[index].Value, want)
		}
	}
}

func TestRepositoryMarkReadLeavesASameSecondNewerMessageUnread(t *testing.T) {
	state := &ticketReadDriverState{affected: 0, exists: 1}
	repo := NewRepository(openTicketReadDB(t, state))

	updated, err := repo.MarkRead(context.Background(), 12, 7, false, "2026-08-15 10:01:02", 4)
	if err != nil || updated {
		t.Fatalf("stale MarkRead() = %v, %v; want false", updated, err)
	}
}

func TestRepositoryMarkReadPreservesOwnerIsolation(t *testing.T) {
	state := &ticketReadDriverState{affected: 0, exists: 0}
	repo := NewRepository(openTicketReadDB(t, state))

	updated, err := repo.MarkRead(context.Background(), 12, 7, false, "2026-08-15 10:01:02", 4)
	if updated || !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("unauthorized MarkRead() = %v, %v; want sql.ErrNoRows", updated, err)
	}
}
