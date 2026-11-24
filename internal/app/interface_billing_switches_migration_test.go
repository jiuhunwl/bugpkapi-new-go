package app

import (
	"os"
	"strings"
	"testing"
)

func TestInterfaceBillingSwitchesMigrationContract(t *testing.T) {
	content, err := os.ReadFile("../../migrations/000096_interface_billing_switches.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := strings.ToLower(string(content))
	for _, column := range []string{"billing_enabled", "billing_log_enabled", "log_visible"} {
		if !strings.Contains(sql, "add column "+column+" tinyint not null default 1") {
			t.Fatalf("missing backward-compatible switch: %s", column)
		}
	}
	if strings.Contains(sql, "delete from") || strings.Contains(sql, "drop table") {
		t.Fatal("billing switch migration must preserve existing records")
	}
}
