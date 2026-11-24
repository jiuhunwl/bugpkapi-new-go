package testdb

import "testing"

func TestValidateIsolatedMySQLDSN(t *testing.T) {
	for _, dsn := range []string{
		"root:root@tcp(127.0.0.1:3306)/bugpk_api_test?parseTime=true",
		"root:root@tcp(127.0.0.1:3306)/bugpk_api_full_e2e?parseTime=true",
		"root:root@tcp(127.0.0.1:3306)/bugpk_api_risk_bench?parseTime=true",
	} {
		if err := ValidateIsolatedMySQLDSN(dsn); err != nil {
			t.Fatalf("isolated DSN %q rejected: %v", dsn, err)
		}
	}
	for _, dsn := range []string{
		"root:root@tcp(127.0.0.1:3306)/bugpk_api?parseTime=true",
		"root:root@tcp(127.0.0.1:3306)/mysql?parseTime=true",
		"not-a-dsn",
	} {
		if err := ValidateIsolatedMySQLDSN(dsn); err == nil {
			t.Fatalf("unsafe DSN %q was accepted", dsn)
		}
	}
}
