package analytics

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
)

type analyticsCaptureDriver struct {
	state *analyticsCaptureState
}

type analyticsCaptureConn struct {
	state *analyticsCaptureState
}

type analyticsCaptureState struct {
	args []driver.NamedValue
}

var analyticsCaptureDriverID atomic.Uint64

func (d analyticsCaptureDriver) Open(string) (driver.Conn, error) {
	return analyticsCaptureConn{state: d.state}, nil
}

func (c analyticsCaptureConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare is not supported")
}

func (c analyticsCaptureConn) Close() error { return nil }

func (c analyticsCaptureConn) Begin() (driver.Tx, error) {
	return nil, errors.New("transactions are not supported")
}

func (c analyticsCaptureConn) ExecContext(_ context.Context, _ string, args []driver.NamedValue) (driver.Result, error) {
	c.state.args = append([]driver.NamedValue(nil), args...)
	return driver.RowsAffected(1), nil
}

func openAnalyticsCaptureDB(t *testing.T, state *analyticsCaptureState) *sql.DB {
	t.Helper()
	name := fmt.Sprintf("analytics-security-%d", analyticsCaptureDriverID.Add(1))
	sql.Register(name, analyticsCaptureDriver{state: state})
	db, err := sql.Open(name, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestRepositoryRecordErrorRedactsUpstreamEndpointBeforePersistence(t *testing.T) {
	state := &analyticsCaptureState{}
	repo := NewRepository(openAnalyticsCaptureDB(t, state))
	processErr := errors.New(`Get "https://parse.upstream.example.com/api/parse?url=https%3A%2F%2Fv.kuaishou.com%2FAbCdEf12": net/http: timeout awaiting response headers`)

	if err := repo.RecordError(context.Background(), processErr); err != nil {
		t.Fatal(err)
	}
	if len(state.args) != 2 {
		t.Fatalf("RecordError persisted %d args, want 2: %#v", len(state.args), state.args)
	}
	lastError, ok := state.args[0].Value.(string)
	if !ok {
		t.Fatalf("last_error type = %T, want string", state.args[0].Value)
	}
	for _, forbidden := range []string{"parse.upstream.example.com", "v.kuaishou.com", "/api/parse", "AbCdEf12"} {
		if strings.Contains(lastError, forbidden) {
			t.Fatalf("analytics last_error leaked %q: %s", forbidden, lastError)
		}
	}
	if !strings.Contains(lastError, "[UPSTREAM_REDACTED]") || !strings.Contains(lastError, "timeout awaiting response headers") {
		t.Fatalf("analytics last_error lost safe diagnostic context: %s", lastError)
	}
}
