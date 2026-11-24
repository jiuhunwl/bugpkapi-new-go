package gateway

import (
	"context"
	"database/sql/driver"
	"strings"
	"testing"
	"time"

	"bugpk-api/internal/pkg/testdb"
)

func TestKeyAuthByHashScansRoleQuotaSnapshot(t *testing.T) {
	now := time.Date(2026, 11, 3, 10, 0, 0, 0, time.Local)
	db := testdb.OpenStub(t, testdb.SQLStub{
		Query: func(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
			for _, fragment := range []string{
				"request_quota_enabled", "request_quota_minute", "new_account_day_limit",
				"bp_user_package_global", "bp_user_package_interface", "bp_user_risk_state", "primary_role.role_key",
			} {
				if !strings.Contains(query, fragment) {
					t.Fatalf("KeyAuthByHash query missing %q", fragment)
				}
			}
			return testdb.Rows([]driver.Value{
				int64(7),      // key id
				int64(41),     // user id
				"alice",       // username
				int64(1),      // account status
				int64(0),      // ban permanent
				"",            // ban until
				"",            // ban reason
				int64(3),      // token version
				"bp_live_abc", // key prefix
				"GLOBAL",      // key type
				int64(1),      // key status
				nil,           // expires at
				int64(1),      // has ip whitelist
				int64(0),      // specified response
				int64(2),      // role id
				"user",        // role key
				now,           // created at
				int64(1),      // quota enabled
				int64(60),     // minute
				int64(600),    // hour
				int64(1000),   // day
				int64(20000),  // month
				"success",     // count mode
				int64(500),    // new account day limit
				int64(1),      // has usable global package
				int64(0),      // has usable interface package
				int64(0),      // quarantined
			}), nil
		},
	})
	key, err := NewRepository(db).KeyAuthByHash(context.Background(), "hash")
	if err != nil {
		t.Fatal(err)
	}
	if key.UserID != 41 || key.RoleID != 2 || key.RoleKey != "user" || key.QuotaEnabled != 1 ||
		key.QuotaMinuteLimit != 60 || key.QuotaHourLimit != 600 ||
		key.QuotaDayLimit != 1000 || key.QuotaMonthLimit != 20000 ||
		key.QuotaCountMode != "success" || key.NewAccountDayLimit != 500 || key.Quarantined ||
		!key.HasUsableGlobalPackage || key.HasUsableInterfacePackage {
		t.Fatalf("scanned key quota = %+v", key)
	}
	if !key.UserCreatedAt.Equal(now) || key.HasIPWhitelist != true {
		t.Fatalf("scanned user metadata = %+v", key)
	}
}
