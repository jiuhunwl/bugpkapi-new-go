package observability

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	"bugpk-api/internal/pkg/testdb"

	_ "github.com/go-sql-driver/mysql"
)

// TestListCallLogsReturnsTheExactTotal guards the regression that previously
// reported at most pageSize*MaxPage rows as the total.  It is deliberately an
// integration test because a fake row count cannot prove SQL semantics.
func TestListCallLogsReturnsTheExactTotal(t *testing.T) {
	dsn := os.Getenv("BUGPK_MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("BUGPK_MYSQL_TEST_DSN is not configured")
	}
	if err := testdb.ValidateIsolatedMySQLDSN(dsn); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(4)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	const (
		interfaceID = int64(99121)
		userID      = int64(1)
		rowCount    = 20_001
		batchSize   = 500
	)
	cleanup := func() {
		_, _ = db.ExecContext(context.Background(), "delete from bp_interface_call_log where interface_id=?", interfaceID)
	}
	cleanup()
	t.Cleanup(cleanup)

	for offset := 0; offset < rowCount; offset += batchSize {
		batch := batchSize
		if remaining := rowCount - offset; remaining < batch {
			batch = remaining
		}
		values := make([]string, batch)
		args := make([]any, 0, batch*2)
		for index := range values {
			values[index] = "(?,?, 'GET',200,1,0,'FREE','NONE',0,0,0,1,NOW())"
			args = append(args, interfaceID, userID)
		}
		query := "insert into bp_interface_call_log(interface_id,user_id,request_method,response_status,success,request_size,charge_type,billing_status,billable,charge_amount,point_price,user_visible,created_at) values " + strings.Join(values, ",")
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("insert batch at offset %d: %v", offset, err)
		}
	}

	result, err := NewRepository(db).ListCallLogs(ctx, CallLogListParams{
		InterfaceID: interfaceID,
		Page:        1,
		PageSize:    20,
	}, true, userID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != rowCount {
		t.Fatalf("exact call-log total = %d, want %d", result.Total, rowCount)
	}
	if len(result.Items) != 20 {
		t.Fatalf("first page item count = %d, want 20", len(result.Items))
	}
	if result.NextCursor == "" {
		t.Fatal("first page must expose a cursor for the next page")
	}

	secondPage, err := NewRepository(db).ListCallLogs(ctx, CallLogListParams{
		Cursor:      result.NextCursor,
		InterfaceID: interfaceID,
		Page:        2,
		PageSize:    20,
	}, true, userID)
	if err != nil {
		t.Fatal(err)
	}
	if secondPage.Total != rowCount {
		t.Fatalf("cursor page total = %d, want the complete %d", secondPage.Total, rowCount)
	}
	if len(secondPage.Items) != 20 {
		t.Fatalf("cursor page item count = %d, want 20", len(secondPage.Items))
	}
	firstPageIDs := make(map[string]struct{}, len(result.Items))
	for _, item := range result.Items {
		firstPageIDs[item.ID] = struct{}{}
	}
	for _, item := range secondPage.Items {
		if _, exists := firstPageIDs[item.ID]; exists {
			t.Fatalf("cursor page repeated first-page row %q", item.ID)
		}
	}
}
