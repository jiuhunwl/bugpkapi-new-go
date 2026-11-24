package app

import (
	"context"
	"database/sql/driver"
	"errors"
	"testing"

	"bugpk-api/internal/pkg/testdb"
)

func TestDatabaseTimeConfirmationBindsActualTargetAndPolicy(t *testing.T) {
	for _, tc := range []struct {
		name, receipt, uuid, database, zone string
		wantOK                              bool
	}{
		{"unconfirmed matching clock", "", "instance-a", "app-a", "+08:00", false},
		{"boolean is not approval", "true", "instance-a", "app-a", "+08:00", false},
		{"other database", databaseTimeConfirmation("instance-a", "app-b"), "instance-a", "app-a", "+08:00", false},
		{"other instance", databaseTimeConfirmation("instance-b", "app-a"), "instance-a", "app-a", "+08:00", false},
		{"wrong policy prefix", "mysql-civil-v0:untrusted", "instance-a", "app-a", "+08:00", false},
		{"approved data", databaseTimeConfirmation("instance-a", "app-a"), "instance-a", "app-a", "+08:00", true},
		{"wrong session even with receipt", databaseTimeConfirmation("instance-a", "app-a"), "instance-a", "app-a", "+00:00", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reads := 0
			db := testdb.OpenStub(t, testdb.SQLStub{Query: func(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
				reads++
				if _, ok := ctx.Deadline(); !ok {
					t.Error("startup policy check is unbounded")
				}
				return testdb.Rows([]driver.Value{tc.uuid, tc.database, tc.zone}), nil
			}, Exec: func(context.Context, string, []driver.NamedValue) (driver.Result, error) {
				t.Error("policy confirmation must not write business or server state")
				return nil, errors.New("unexpected write")
			}})
			err := validateDatabaseTimePolicy(context.Background(), db, tc.receipt)
			if (err == nil) != tc.wantOK {
				t.Fatalf("incorrect policy decision: %v", err)
			}
			if reads != 1 {
				t.Fatalf("target identity was not read exactly once: %d", reads)
			}
		})
	}
}

func TestDatabaseTimePolicyDoesNotTrustReceiptWhenIdentityUnreadable(t *testing.T) {
	failure := errors.New("identity unavailable")
	db := testdb.OpenStub(t, testdb.SQLStub{Query: func(context.Context, string, []driver.NamedValue) (driver.Rows, error) { return nil, failure }})
	if err := validateDatabaseTimePolicy(context.Background(), db, databaseTimeConfirmation("instance-a", "app-a")); !errors.Is(err, failure) {
		t.Fatalf("unreadable identity was bypassed: %v", err)
	}
}
