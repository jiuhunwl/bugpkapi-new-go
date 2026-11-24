package notification

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type notificationCaptureDriver struct {
	state *notificationCaptureState
}

type notificationCaptureConn struct {
	state *notificationCaptureState
}

type notificationCaptureState struct {
	args []driver.NamedValue
}

var notificationCaptureDriverID atomic.Uint64

func (d notificationCaptureDriver) Open(string) (driver.Conn, error) {
	return notificationCaptureConn{state: d.state}, nil
}

func (c notificationCaptureConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare is not supported")
}

func (c notificationCaptureConn) Close() error { return nil }

func (c notificationCaptureConn) Begin() (driver.Tx, error) {
	return nil, errors.New("transactions are not supported")
}

func (c notificationCaptureConn) ExecContext(_ context.Context, _ string, args []driver.NamedValue) (driver.Result, error) {
	c.state.args = append([]driver.NamedValue(nil), args...)
	return driver.RowsAffected(1), nil
}

func openNotificationCaptureDB(t *testing.T, state *notificationCaptureState) *sql.DB {
	t.Helper()
	name := fmt.Sprintf("notification-security-%d", notificationCaptureDriverID.Add(1))
	sql.Register(name, notificationCaptureDriver{state: state})
	db, err := sql.Open(name, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestRepositoryMarkRetryRedactsUpstreamEndpointBeforePersistence(t *testing.T) {
	state := &notificationCaptureState{}
	repo := NewRepository(openNotificationCaptureDB(t, state))
	deliveryErr := errors.New(`Get "https://parse.upstream.example.com/api/parse?url=https%3A%2F%2Fv.kuaishou.com%2FAbCdEf12": net/http: timeout awaiting response headers`)

	if err := repo.MarkRetry(context.Background(), Entry{ID: 42, Attempts: 1, MaxAttempts: 12}, deliveryErr, time.Now(), false); err != nil {
		t.Fatal(err)
	}
	if len(state.args) != 4 {
		t.Fatalf("MarkRetry persisted %d args, want 4: %#v", len(state.args), state.args)
	}
	lastError, ok := state.args[2].Value.(string)
	if !ok {
		t.Fatalf("last_error type = %T, want string", state.args[2].Value)
	}
	for _, forbidden := range []string{"parse.upstream.example.com", "v.kuaishou.com", "/api/parse", "AbCdEf12"} {
		if strings.Contains(lastError, forbidden) {
			t.Fatalf("notification last_error leaked %q: %s", forbidden, lastError)
		}
	}
	if !strings.Contains(lastError, "[UPSTREAM_REDACTED]") || !strings.Contains(lastError, "timeout awaiting response headers") {
		t.Fatalf("notification last_error lost safe diagnostic context: %s", lastError)
	}
}
