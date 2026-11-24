package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRiskControlOverviewSnapshotMigrationContract(t *testing.T) {
	contents, err := os.ReadFile(filepath.Join(sqlArtifactRoot(), "migrations", "000088_risk_control_overview_snapshot.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	sql := strings.ToLower(string(contents))
	for _, required := range []string{
		"create table if not exists `bp_risk_control_overview_snapshot`",
		"`window_minutes` smallint unsigned not null",
		"`payload` json not null",
		"primary key (`window_minutes`)",
		"drop index `idx_bp_call_risk_single`",
		"add index `idx_bp_call_risk_single` (`bucket_start`, `client_ip_hash`, `interface_id`, `request_fingerprint`, `has_client_ip`, `call_count`)",
		"add index `idx_bp_call_risk_proxy` (`bucket_start`, `user_id`, `interface_id`, `request_fingerprint`, `has_client_ip`, `client_ip_hash`, `call_count`)",
		"add index `idx_bp_call_risk_distributed` (`bucket_start`, `interface_id`, `request_fingerprint`, `has_client_ip`, `client_ip_hash`, `user_id`, `call_count`)",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("migration 88 missing %q", required)
		}
	}
}

func TestFullSQLIncludesRiskSnapshot(t *testing.T) {
	sql := readDistributionSQL(t, "full")
	assertContainsAll(t, "full SQL", sql, []string{
		"bp_risk_control_overview_snapshot",
		"idx_bp_call_risk_single` (`bucket_start`, `client_ip_hash`, `interface_id`, `request_fingerprint`, `has_client_ip`, `call_count`)",
	})
}
