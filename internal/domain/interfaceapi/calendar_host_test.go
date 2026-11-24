package interfaceapi

import (
	"context"
	"database/sql/driver"
	"testing"
	"time"

	"bugpk-api/internal/pkg/testdb"
	"bugpk-api/internal/pkg/testhost"
)

func TestMain(m *testing.M) { testhost.Main(m) }
func TestInterfaceTrendUsesBusinessCalendarAcrossHosts(t *testing.T) {
	testhost.Run(t, "TestInterfaceTrendCalendarChild")
}

func TestInterfaceTrendCalendarChild(t *testing.T) {
	if !testhost.IsChild() {
		t.Skip("run by host harness")
	}
	expectedZone := time.FixedZone("expected +08", 8*60*60)
	before := time.Now().In(expectedZone)
	rows := [][]driver.Value{}
	for i := -7; i <= 1; i++ {
		rows = append(rows, []driver.Value{before.AddDate(0, 0, i).Format("2006-01-02"), int64(9)})
	}
	var queryStart, queryEnd string
	db := testdb.OpenStub(t, testdb.SQLStub{Query: func(_ context.Context, _ string, args []driver.NamedValue) (driver.Rows, error) {
		if len(args) != 3 || args[0].Value != int64(42) {
			t.Fatalf("interface scope was lost: %v", args)
		}
		queryStart, _ = args[1].Value.(string)
		queryEnd, _ = args[2].Value.(string)
		return testdb.Rows(rows...), nil
	}})
	got, err := NewRepository(db).InterfaceTrend7d(context.Background(), 42)
	if err != nil {
		t.Fatal(err)
	}
	after := time.Now().In(expectedZone)
	if len(got) != 7 {
		t.Fatalf("expected seven civil days, got %d", len(got))
	}
	if queryStart != got[0]["date"] {
		t.Fatalf("SQL lower bound differs from displayed grid: %s %+v", queryStart, got)
	}
	start, startErr := time.ParseInLocation("2006-01-02", queryStart, expectedZone)
	end, endErr := time.ParseInLocation("2006-01-02", queryEnd, expectedZone)
	if startErr != nil || endErr != nil || end.Sub(start) != 7*24*time.Hour {
		t.Fatalf("SQL is not bounded to the displayed seven days: %s %s", queryStart, queryEnd)
	}
	latest := got[6]["date"]
	if latest != before.Format("2006-01-02") && latest != after.Format("2006-01-02") {
		t.Fatalf("trend ends on host day instead of stored day: %v", got)
	}
	if got[6]["value"] != int64(9) {
		t.Fatalf("business-day count was dropped during index merge: %v", got[6])
	}
}
