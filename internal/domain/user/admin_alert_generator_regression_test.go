package user

import (
	"context"
	"database/sql/driver"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bugpk-api/internal/pkg/iphash"
	"bugpk-api/internal/pkg/testdb"
)

func TestAlertAccessAnomaliesUsesFailureRollupClientIPAndHashesDisplayValue(t *testing.T) {
	var query string
	db := testdb.OpenStub(t, testdb.SQLStub{
		Query: func(_ context.Context, q string, _ []driver.NamedValue) (driver.Rows, error) {
			query = q
			return testdb.Rows([]driver.Value{int64(100), strings.Repeat("a", 64)}), nil
		},
	})

	alerts, err := NewRepository(db).alertAccessAnomalies(context.Background())
	if err != nil {
		t.Fatalf("alertAccessAnomalies() error = %v", err)
	}
	if len(alerts) != 1 {
		t.Fatalf("alertAccessAnomalies() alerts = %+v, want one alert", alerts)
	}

	lower := strings.ToLower(query)
	for _, required := range []string{
		"from bp_call_failure_ip_hour",
		"client_ip",
		"group by client_ip",
		"sha2(concat('" + iphash.FailureSQLPrefix() + "',client_ip),256)",
	} {
		if !strings.Contains(lower, strings.ToLower(required)) {
			t.Fatalf("alert query missing %q: %s", required, query)
		}
	}
	for _, forbidden := range []string{"client_ip_hash", "hex(client_ip_hash)", "select client_ip"} {
		if strings.Contains(lower, forbidden) {
			t.Fatalf("alert query exposes or references invalid IP field %q: %s", forbidden, query)
		}
	}
	if strings.Contains(alerts[0].Content, "203.0.113.10") {
		t.Fatalf("alert content leaked a plaintext IP: %q", alerts[0].Content)
	}
	if !strings.Contains(alerts[0].Content, "指纹 "+strings.Repeat("a", 12)+"…") {
		t.Fatalf("alert content did not expose only the short hash prefix: %q", alerts[0].Content)
	}
	if alerts[0].URL != "/risk-control/dashboard" {
		t.Fatalf("access alert target = %q, want risk control dashboard", alerts[0].URL)
	}
}

func TestBacklogAlertsTargetExistingAdminPages(t *testing.T) {
	db := testdb.OpenStub(t, testdb.SQLStub{Query: func(_ context.Context, _ string, _ []driver.NamedValue) (driver.Rows, error) {
		return testdb.Rows([]driver.Value{int64(20)}), nil
	}})

	alerts, err := NewRepository(db).alertBacklog(context.Background())
	if err != nil {
		t.Fatalf("alertBacklog() error = %v", err)
	}
	want := map[string]string{
		"工单积压待处理": "/admin/tickets",
		"存在未完成订单": "/payment/orders",
		"友链申请待审核": "/content/friend-link",
	}
	if len(alerts) != len(want) {
		t.Fatalf("alertBacklog() alerts = %+v, want %d alerts", alerts, len(want))
	}
	for _, alert := range alerts {
		if target, ok := want[alert.Title]; !ok || alert.URL != target {
			t.Fatalf("backlog alert %q target = %q, want %q", alert.Title, alert.URL, target)
		}
	}
}

func TestFailureIPRollupMigrationMatchesAlertQuerySchema(t *testing.T) {
	contents, err := os.ReadFile(filepath.Join("..", "..", "..", "migrations", "000078_call_log_rollups.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	sqlText := strings.ToLower(string(contents))
	start := strings.Index(sqlText, "create table if not exists `bp_call_failure_ip_hour`")
	if start < 0 {
		t.Fatal("cannot find bp_call_failure_ip_hour schema")
	}
	relativeEnd := strings.Index(sqlText[start:], "create table if not exists `bp_call_failure_user_hour`")
	if relativeEnd < 0 {
		t.Fatal("cannot isolate bp_call_failure_ip_hour schema")
	}
	failureSchema := sqlText[start : start+relativeEnd]
	if !strings.Contains(failureSchema, "`client_ip` varchar(64)") {
		t.Fatalf("failure IP rollup does not declare client_ip: %s", failureSchema)
	}
	if strings.Contains(failureSchema, "client_ip_hash") {
		t.Fatalf("failure IP rollup unexpectedly declares client_ip_hash: %s", failureSchema)
	}
}
