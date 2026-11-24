package interfaceapi

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"
)

type requestIDLogRecorder struct {
	query string
	args  []driver.NamedValue
}

func (r *requestIDLogRecorder) Connect(context.Context) (driver.Conn, error) { return r, nil }
func (r *requestIDLogRecorder) Driver() driver.Driver                        { return r }
func (r *requestIDLogRecorder) Open(string) (driver.Conn, error)             { return r, nil }
func (r *requestIDLogRecorder) Close() error                                 { return nil }
func (r *requestIDLogRecorder) Begin() (driver.Tx, error)                    { return r, nil }
func (r *requestIDLogRecorder) Commit() error                                { return nil }
func (r *requestIDLogRecorder) Rollback() error                              { return nil }
func (r *requestIDLogRecorder) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected prepare")
}
func (r *requestIDLogRecorder) ExecContext(_ context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	if strings.Contains(q, "insert into bp_interface_call_log(") {
		r.query = q
		r.args = append([]driver.NamedValue(nil), args...)
	}
	// The body side-table insert is recorded but does not override the main
	// insert captured above (the assertions target the main log row).
	return &requestIDResult{lastID: 1}, nil
}

type requestIDResult struct{ lastID int64 }

func (r *requestIDResult) LastInsertId() (int64, error) { return r.lastID, nil }
func (r *requestIDResult) RowsAffected() (int64, error) { return 1, nil }

func TestManagementCallLogPersistsFeedbackRequestID(t *testing.T) {
	recorder := &requestIDLogRecorder{}
	db := sql.OpenDB(recorder)
	defer db.Close()
	repo := NewRepository(db)
	err := repo.CreateCallLog(context.Background(), InterfaceItem{ID: "1"}, Principal{ID: 42}, 0, "", InvokeResult{RequestID: "gw_feedback", Body: `{"code":200}`, RequestMethod: "GET", StatusCode: 200}, "{}", "", "", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(recorder.query, "trace_id)") {
		t.Fatalf("request ID column missing: %s", recorder.query)
	}
	if strings.Count(recorder.query, "?") != len(recorder.args) {
		t.Fatalf("SQL argument count mismatch: %d placeholders, %d args", strings.Count(recorder.query, "?"), len(recorder.args))
	}
	if recorder.args[len(recorder.args)-1].Value != "gw_feedback" {
		t.Fatalf("request ID not bound: %#v", recorder.args)
	}
}
