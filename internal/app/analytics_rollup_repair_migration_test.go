package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAnalyticsRollupRepairMigrationContract(t *testing.T) {
	repoRoot := sqlArtifactRoot()
	upPath := filepath.Join(repoRoot, "migrations", "000090_analytics_rollup_repair.up.sql")
	downPath := filepath.Join(repoRoot, "migrations", "000090_analytics_rollup_repair.down.sql")
	up, err := os.ReadFile(upPath)
	if err != nil {
		t.Fatal(err)
	}
	down, err := os.ReadFile(downPath)
	if err != nil {
		t.Fatal(err)
	}

	upSQL := strings.ToLower(string(up))
	for _, fragment := range []string{
		"bp_analytics_checkpoint",
		"repair_status",
		"repair_window_days",
		"repair_requested_at",
		"repair_started_at",
		"repair_completed_at",
		"'pending'",
		"interval 31 day",
	} {
		if !strings.Contains(upSQL, fragment) {
			t.Fatalf("analytics repair migration missing %q", fragment)
		}
	}
	if strings.Contains(upSQL, "from bp_interface_call_log") {
		t.Fatalf("analytics repair migration must not aggregate the raw call log: %s", upSQL)
	}

	downSQL := strings.ToLower(string(down))
	for _, fragment := range []string{
		"repair_status",
		"repair_window_days",
		"repair_requested_at",
		"repair_started_at",
		"repair_completed_at",
	} {
		if !strings.Contains(downSQL, fragment) {
			t.Fatalf("analytics repair down migration missing %q", fragment)
		}
	}
}

func TestAnalyticsRollupRepairReleaseContract(t *testing.T) {
	full := readDistributionSQL(t, "full")
	assertContainsAll(t, "full SQL", full, []string{
		"repair_status",
		"repair_completed_at",
	})
}
