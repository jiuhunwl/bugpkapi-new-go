package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAnalyticsRepairCursorMigrationIsMetadataOnlyAndResumable(t *testing.T) {
	repoRoot := sqlArtifactRoot()
	upPath := filepath.Join(repoRoot, "migrations", "000092_analytics_rollup_repair_cursor.up.sql")
	downPath := filepath.Join(repoRoot, "migrations", "000092_analytics_rollup_repair_cursor.down.sql")
	up, err := os.ReadFile(upPath)
	if err != nil {
		t.Fatal(err)
	}
	down, err := os.ReadFile(downPath)
	if err != nil {
		t.Fatal(err)
	}
	upSQL := strings.ToLower(string(up))
	downSQL := strings.ToLower(string(down))
	for _, fragment := range []string{
		"repair_upper_log_id",
		"repair_cursor_id",
		"repair_projection_mask",
		"repair_phase",
		"repair_window_start",
		"repair_clear_projection",
		"'pending'",
		"'clear'",
	} {
		if !strings.Contains(upSQL, fragment) {
			t.Fatalf("analytics repair cursor migration missing %q", fragment)
		}
	}
	for _, forbidden := range []string{
		"from `bp_interface_call_log`",
		"from bp_interface_call_log",
		"delete from bp_call_",
	} {
		if strings.Contains(upSQL, forbidden) {
			t.Fatalf("migration must only persist repair metadata, found %q", forbidden)
		}
	}
	for _, fragment := range []string{
		"repair_upper_log_id",
		"repair_cursor_id",
		"repair_projection_mask",
		"repair_phase",
		"repair_window_start",
		"repair_clear_projection",
	} {
		if !strings.Contains(downSQL, fragment) {
			t.Fatalf("analytics repair cursor down migration missing %q", fragment)
		}
	}
}

func TestAnalyticsRepairCursorReleaseContract(t *testing.T) {
	full := readDistributionSQL(t, "full")
	assertContainsAll(t, "full SQL", full, []string{
		"repair_projection_mask",
		"repair_phase",
	})
}
