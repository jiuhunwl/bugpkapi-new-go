package user

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"
	"time"

	"bugpk-api/internal/pkg/testdb"
)

func TestAdminCallTrendKeepsCompleteRollupWithGenuineZeroDays(t *testing.T) {
	db := sql.OpenDB(partialTrendConnector{})
	defer db.Close()
	trend, err := NewRepository(db).AdminCallTrend(withoutAdminRawLogFallback(context.Background()), []string{"10-01", "10-02", "10-03"})
	if err != nil {
		t.Fatalf("valid rollup must survive optional gap fallback: %v", err)
	}
	if trend.Total != 17 || trend.TotalCalls[len(trend.TotalCalls)-1] != 17 || trend.SuccessCalls[len(trend.SuccessCalls)-1] != 15 || trend.FailedCalls[len(trend.FailedCalls)-1] != 2 {
		t.Fatalf("lost genuine rollup: %+v", trend)
	}
}

type partialTrendConnector struct{}

func (partialTrendConnector) Connect(context.Context) (driver.Conn, error) {
	return partialTrendConn{}, nil
}
func (partialTrendConnector) Driver() driver.Driver { return partialTrendDriver{} }

type partialTrendDriver struct{}

func (partialTrendDriver) Open(string) (driver.Conn, error) { return partialTrendConn{}, nil }

type partialTrendConn struct{}

func (partialTrendConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected prepare")
}
func (partialTrendConn) Close() error              { return nil }
func (partialTrendConn) Begin() (driver.Tx, error) { return nil, errors.New("unexpected transaction") }
func (partialTrendConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	return testdb.ReadOnlyTransaction(ctx, opts)
}
func (partialTrendConn) QueryContext(_ context.Context, q string, _ []driver.NamedValue) (driver.Rows, error) {
	if strings.Contains(q, "c.last_log_id,c.last_completed_at") {
		return testdb.Rows([]driver.Value{int64(17), time.Now(), "", "NONE", "IDLE", int64(0), int64(17), time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)}), nil
	}
	if strings.Contains(q, "last_completed_at") {
		return &adminWorkbenchFailureRows{columns: []string{"last_completed_at"}, values: [][]driver.Value{{time.Now()}}}, nil
	}
	if strings.Contains(q, "from bp_call_metric_day") {
		return &adminWorkbenchFailureRows{columns: []string{"day", "calls", "success", "failed", "billable", "amount", "projected"}, values: [][]driver.Value{{"10-03", int64(17), int64(15), int64(2), int64(3), "0.3000", int64(17)}}}, nil
	}
	if strings.Contains(q, "from bp_call_api_metric_day") {
		return testdb.Rows([]driver.Value{"10-03", int64(17)}), nil
	}
	if strings.Contains(q, "group by date_format(l.created_at") {
		return testdb.Rows([]driver.Value{"10-03", int64(17)}), nil
	}
	if strings.Contains(q, "from bp_call_metric_minute") && !strings.Contains(q, "source.bucket_start") {
		return testdb.Rows([]driver.Value{"10-03", int64(17)}), nil
	}
	if strings.Contains(q, "select exists") {
		return &adminWorkbenchFailureRows{columns: []string{"exists"}, values: [][]driver.Value{{int64(0)}}}, nil
	}
	return nil, errors.New("unexpected raw fallback: " + q)
}
