package ticket

import (
	"context"
	"database/sql/driver"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"bugpk-api/internal/pkg/testdb"
	"bugpk-api/internal/pkg/testhost"
)

func TestMain(m *testing.M) { testhost.Main(m) }
func TestTicketStatisticsUseBusinessCalendarAcrossHosts(t *testing.T) {
	testhost.Run(t, "TestTicketStatisticsCalendarChild")
}

func TestTicketStatisticsCalendarChild(t *testing.T) {
	if !testhost.IsChild() {
		t.Skip("run by host harness")
	}
	expectedZone := time.FixedZone("expected +08", 8*60*60)
	before := time.Now().In(expectedZone).AddDate(0, 0, -6).Format("2006-01-02 00:00:00")
	var checked atomic.Int32
	db := testdb.OpenStub(t, testdb.SQLStub{Query: func(_ context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
		after := time.Now().In(expectedZone).AddDate(0, 0, -6).Format("2006-01-02 00:00:00")
		for _, arg := range args {
			if value, ok := arg.Value.(string); ok && len(value) == 19 {
				checked.Add(1)
				if value != before && value != after {
					t.Errorf("ticket lower bound uses host date: %q expected %q", value, before)
				}
			}
		}
		if strings.Contains(q, "select count(*)") {
			return testdb.Rows([]driver.Value{int64(0), int64(0), int64(0), int64(0), int64(0), nil, nil}), nil
		}
		return testdb.Rows(), nil
	}})
	if _, err := NewRepository(db).Statistics(context.Background(), 7); err != nil {
		t.Fatal(err)
	}
	if checked.Load() < 3 {
		t.Fatal("statistics query bounds were not exercised")
	}
}
