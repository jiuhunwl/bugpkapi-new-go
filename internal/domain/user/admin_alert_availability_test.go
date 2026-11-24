package user

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"bugpk-api/internal/pkg/testdb"
)

func TestAdminAlertsKeepSuccessfulSourcesWhenOthersFail(t *testing.T) {
	for _, count := range []int64{0, 20} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			db := testdb.OpenStub(t, testdb.SQLStub{Query: func(_ context.Context, q string, _ []driver.NamedValue) (driver.Rows, error) {
				if strings.Contains(q, "from bp_ticket") {
					return testdb.Rows([]driver.Value{count}), nil
				}
				return nil, errors.New("source unavailable")
			}})
			got, err := NewRepository(db).adminWorkbenchAlerts(context.Background(), time.Now())
			if err != nil {
				t.Fatalf("successful backlog source discarded: %v", err)
			}
			if count == 0 && len(got) != 0 {
				t.Fatalf("real zero source fabricated alerts: %+v", got)
			}
			if count == 20 && (len(got) != 1 || got[0].Title != "工单积压待处理") {
				t.Fatalf("healthy ticket alert lost: %+v", got)
			}
		})
	}
}
