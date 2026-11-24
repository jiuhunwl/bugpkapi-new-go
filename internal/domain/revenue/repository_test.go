package revenue

import (
	"context"
	"database/sql/driver"
	"errors"
	"testing"

	"bugpk-api/internal/pkg/testdb"
	mysql "github.com/go-sql-driver/mysql"
)

func TestTotalFallbackIsLimitedToMissingProjectionSchema(t *testing.T) {
	for _, tc := range []struct {
		name     string
		err      error
		fallback bool
	}{
		{"projection absent", &mysql.MySQLError{Number: 1146, Message: "Table 'test.bp_billing_revenue_projection' doesn't exist"}, true},
		{"marker absent", &mysql.MySQLError{Number: 1054, Message: "Unknown column 'b.revenue_projected'"}, true},
		{"ledger absent", &mysql.MySQLError{Number: 1146, Message: "Table 'test.bp_billing_record' doesn't exist"}, false},
		{"index absent", &mysql.MySQLError{Number: 1176, Message: "Key idx_bp_billing_revenue_pending doesn't exist"}, false},
		{"denied", errors.New("permission denied"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			queries := 0
			db := testdb.OpenStub(t, testdb.SQLStub{Begin: testdb.ReadOnlyTransaction, Query: func(_ context.Context, q string, _ []driver.NamedValue) (driver.Rows, error) {
				queries++
				if queries == 1 {
					if q != TotalQuery {
						t.Error("wrong read contract")
					}
					return nil, tc.err
				}
				if q != SourceTotalQuery {
					t.Error("wrong fallback")
				}
				return testdb.Rows([]driver.Value{"12.3400"}), nil
			}})
			amount, err := NewRepository(db).Total(context.Background())
			if tc.fallback {
				if err != nil || amount != "12.3400" || queries != 2 {
					t.Fatalf("bad fallback: %s %v queries=%d", amount, err, queries)
				}
			} else if !errors.Is(err, tc.err) || queries != 1 {
				t.Fatalf("error was hidden: %v queries=%d", err, queries)
			}
		})
	}
}
func TestMissingProjectionRowUsesLedgerRatherThanInventedZero(t *testing.T) {
	count := 0
	db := testdb.OpenStub(t, testdb.SQLStub{Begin: testdb.ReadOnlyTransaction, Query: func(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
		count++
		if count == 1 {
			return testdb.Rows(), nil
		}
		return testdb.Rows([]driver.Value{"25.0000"}), nil
	}})
	value, err := NewRepository(db).Total(context.Background())
	if err != nil || value != "25.0000" {
		t.Fatalf("missing metadata hid real revenue: %s %v", value, err)
	}
}
