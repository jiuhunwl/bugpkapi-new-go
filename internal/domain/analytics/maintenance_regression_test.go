package analytics

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDailyRepairCoversOldestRetainedSourceDay(t *testing.T) {
	want := "date_sub(current_date(), interval 3 day)"
	for _, d := range repairProjectionDescriptors {
		if d.index != projectionAPIDay && d.index != projectionMetricDay && d.index != projectionUserMetricDay {
			continue
		}
		if d.repairWindowSQL(31) != want {
			t.Fatalf("%s cannot rebuild oldest surviving day: %s", d.name, d.repairWindowSQL(31))
		}
	}
}

func TestIngestDoesNotRunHeavySourceCoverageAudit(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("repository.go"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	start := strings.Index(s, "func (r *Repository) probeRollupStateIfDue(")
	end := strings.Index(s[start:], "// Checkpoint returns")
	probe := s[start : start+end]
	if !strings.Contains(probe, "probeRollupState(ctx, tx, checkpointID, newestLogID, false)") {
		t.Fatal("ingest still runs the million-row source coverage audit while holding the checkpoint lock")
	}
}

func TestRetentionCannotEraseRepairSourceOrPendingTail(t *testing.T) {
	// Each prune batch now commits in its own READ COMMITTED transaction, so the
	// assertion reads the captured statements instead of a single Exec callback.
	db, statements := pruneStubDB(t, func(int) (int64, error) { return 0, nil })
	r := NewRepository(db)
	if _, err := r.pruneSweepTarget(context.Background(), datasetSweepTargets[0]); err != nil {
		t.Fatal(err)
	}
	if len(*statements) == 0 {
		t.Fatal("log prune issued no delete")
	}
	query := (*statements)[0]
	for _, part := range []string{"last_log_id", "repair_status", "PENDING", "RUNNING", "repair_phase", "CLEAR", "ROLLUP", "FINALIZE"} {
		if !strings.Contains(query, part) {
			t.Fatalf("log prune can destroy unreplayed source; missing %s: %s", part, query)
		}
	}
}
func TestMinuteRetentionProtectsSourceFloorDuringRepair(t *testing.T) {
	db, statements := pruneStubDB(t, func(int) (int64, error) { return 0, nil })
	r := NewRepository(db)
	if _, err := r.pruneSweepTarget(context.Background(), datasetSweepTargets[1]); err != nil {
		t.Fatal(err)
	}
	if len(*statements) == 0 {
		t.Fatal("minute prune issued no delete")
	}
	q := (*statements)[0]
	for _, part := range []string{"bp_analytics_checkpoint", "repair_status", "repair_phase", "min(created_at)", "least("} {
		if !strings.Contains(q, part) {
			t.Fatalf("minute prune can move ahead of protected source: missing %s in %s", part, q)
		}
	}
}
