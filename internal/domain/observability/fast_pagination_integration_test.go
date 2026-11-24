package observability

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"bugpk-api/internal/pkg/testdb"
)

func TestFastCallLogPaginationAcrossMonths(t *testing.T) {
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
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	const interfaceID = 99122
	var count int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM bp_interface_call_log WHERE interface_id=?", interfaceID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("dedicated fixture interface already has logs; refusing to replace them")
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), "DELETE FROM bp_interface_call_log WHERE interface_id=?", interfaceID)
	})
	for _, date := range []string{"2026-08-31 23:59:59", "2026-09-01 00:00:00", "2026-09-02 00:00:00"} {
		for n := 0; n < 2; n++ {
			if _, err := db.ExecContext(ctx, "INSERT INTO bp_interface_call_log(interface_id,user_id,request_method,created_at) VALUES (?,1,'GET',?)", interfaceID, date); err != nil {
				t.Fatal(err)
			}
		}
	}
	repo := NewRepository(db)
	params := CallLogListParams{InterfaceID: interfaceID, Page: 1, PageSize: 3, StartTime: "2026-08-31 00:00:00", EndTime: "2026-09-03 00:00:00"}
	exact, err := repo.ListCallLogs(ctx, params, true, 1)
	if err != nil || exact.Total != 6 {
		t.Fatalf("exact page: total=%d err=%v", exact.Total, err)
	}
	params.SkipTotal = true
	first, err := repo.ListCallLogs(ctx, params, true, 1)
	if err != nil || first.Total != -1 || !first.TotalOmitted || len(first.Items) != 3 || first.NextCursor == "" {
		t.Fatalf("fast first page: %+v err=%v", first, err)
	}
	params.Cursor = first.NextCursor
	second, err := repo.ListCallLogs(ctx, params, true, 1)
	if err != nil || len(second.Items) != 3 || second.NextCursor != "" {
		t.Fatalf("fast second page: %+v err=%v", second, err)
	}
	seen := map[string]bool{}
	for _, item := range append(first.Items, second.Items...) {
		if seen[item.ID] {
			t.Fatalf("duplicate row across month cursor: %s", item.ID)
		}
		seen[item.ID] = true
	}
	for index, item := range exact.Items {
		if first.Items[index].ID != item.ID {
			t.Fatal("fast mode changed filtering or ordering")
		}
	}
	t.Log("actual repository pagination across the month boundary returned all 6 unique rows with unchanged ordering")
}
