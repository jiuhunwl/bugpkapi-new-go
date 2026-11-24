package analytics

import (
	"database/sql"
	"testing"
	"time"
)

// diagnosticsNow is a fixed reference clock so every age comparison in these
// tests is deterministic.
var diagnosticsNow = time.Date(2026, 10, 13, 19, 20, 0, 0, time.UTC)

func diagnosticsTime(offset time.Duration) sql.NullTime {
	return sql.NullTime{Time: diagnosticsNow.Add(offset), Valid: true}
}

func TestDiagnosticsRepairActiveMatchesReaderSemantics(t *testing.T) {
	cases := []struct {
		name   string
		status string
		phase  string
		want   bool
	}{
		{name: "pending status", status: "PENDING", phase: "IDLE", want: true},
		{name: "running status", status: "RUNNING", phase: "IDLE", want: true},
		{name: "clear phase alone", status: "COMPLETE", phase: "CLEAR", want: true},
		{name: "rollup phase alone", status: "NONE", phase: "ROLLUP", want: true},
		{name: "finalize phase alone", status: "NONE", phase: "FINALIZE", want: true},
		{name: "completed", status: "COMPLETE", phase: "IDLE", want: false},
		{name: "never requested", status: "NONE", phase: "", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			item := Diagnostics{RepairStatus: tc.status, RepairPhase: tc.phase}
			if got := item.RepairActive(); got != tc.want {
				t.Fatalf("RepairActive() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestDiagnosticsBehindTreatsEmptyDatasetAsCurrent guards the zero-is-an-answer
// rule: with no source rows at all the rollup cannot be behind, because there is
// nothing to publish yet.
func TestDiagnosticsBehindTreatsEmptyDatasetAsCurrent(t *testing.T) {
	if (Diagnostics{}).Behind() {
		t.Fatal("an empty source dataset was reported as behind")
	}
	if !(Diagnostics{LastLogID: 848, NewestLogID: 1_809_129}).Behind() {
		t.Fatal("a frozen watermark was not reported as behind")
	}
	if (Diagnostics{LastLogID: 100, NewestLogID: 100}).Behind() {
		t.Fatal("a caught-up watermark was reported as behind")
	}
}

func TestDiagnosticsLagSecondsMeasuresSourceGapNotWallClock(t *testing.T) {
	if got := (Diagnostics{}).LagSeconds(); got != 0 {
		t.Fatalf("lag without timestamps = %d, want 0", got)
	}
	if got := (Diagnostics{LastLogCreatedAt: diagnosticsTime(0), NewestLogCreatedAt: diagnosticsTime(0)}).LagSeconds(); got != 0 {
		t.Fatalf("lag for equal timestamps = %d, want 0", got)
	}
	// The watermark is newer than the newest source row (a rebuilt dataset):
	// the lag must clamp to zero instead of going negative.
	if got := (Diagnostics{LastLogCreatedAt: diagnosticsTime(time.Hour), NewestLogCreatedAt: diagnosticsTime(0)}).LagSeconds(); got != 0 {
		t.Fatalf("lag for an ahead watermark = %d, want 0", got)
	}
	item := Diagnostics{
		LastLogCreatedAt:   diagnosticsTime(-17*time.Hour - 46*time.Minute - 51*time.Second),
		NewestLogCreatedAt: diagnosticsTime(0),
	}
	if got, want := item.LagSeconds(), int64(17*3600+46*60+51); got != want {
		t.Fatalf("lag = %d, want %d", got, want)
	}
}

func TestDiagnosticsDiagnoseOrdering(t *testing.T) {
	healthy := Diagnostics{
		CheckpointPresent:    true,
		RepairColumnsPresent: true,
		LastLogID:            1_809_129,
		LastLogCreatedAt:     diagnosticsTime(-time.Minute),
		LastCompletedAt:      diagnosticsTime(-time.Minute),
		NewestLogID:          1_809_129,
		NewestLogCreatedAt:   diagnosticsTime(0),
	}

	cases := []struct {
		name string
		item Diagnostics
		in   DiagnosticInput
		want DiagnosticCause
	}{
		{
			name: "healthy",
			item: healthy,
			in:   DiagnosticInput{WorkerEnabled: true, Now: diagnosticsNow},
			want: CauseHealthy,
		},
		{
			// A disabled worker outranks every other observation: nothing else
			// can be fixed while the pipeline is switched off.
			name: "disabled worker outranks a missing checkpoint",
			item: Diagnostics{},
			in:   DiagnosticInput{WorkerEnabled: false, Now: diagnosticsNow},
			want: CauseWorkerDisabled,
		},
		{
			name: "missing checkpoint",
			item: Diagnostics{},
			in:   DiagnosticInput{WorkerEnabled: true, Now: diagnosticsNow},
			want: CauseCheckpointMissing,
		},
		{
			name: "active repair without a request timestamp can never be aged out",
			item: Diagnostics{CheckpointPresent: true, RepairColumnsPresent: true, RepairStatus: "PENDING"},
			in:   DiagnosticInput{WorkerEnabled: true, Now: diagnosticsNow},
			want: CauseRepairStateUnaged,
		},
		{
			name: "requested repair that never started",
			item: Diagnostics{
				CheckpointPresent: true, RepairColumnsPresent: true,
				RepairStatus: "PENDING", RepairRequestedAt: diagnosticsTime(-time.Hour),
			},
			in:   DiagnosticInput{WorkerEnabled: true, Now: diagnosticsNow},
			want: CauseWorkerNeverRan,
		},
		{
			// repository.go stamps repair_started_at only when the worker
			// actually picks the request up, so a fresh request that has no
			// start time means the worker is not running at all. It is checked
			// before the stall window, which would otherwise mask it after
			// adminAnalyticsRepairStallAfter.
			name: "a long-requested repair that never started still points at the worker",
			item: Diagnostics{
				CheckpointPresent: true, RepairColumnsPresent: true,
				RepairStatus: "PENDING", RepairRequestedAt: diagnosticsTime(-4 * time.Hour),
			},
			in:   DiagnosticInput{WorkerEnabled: true, Now: diagnosticsNow},
			want: CauseWorkerNeverRan,
		},
		{
			name: "stalled repair",
			item: Diagnostics{
				CheckpointPresent: true, RepairColumnsPresent: true,
				RepairStatus: "RUNNING", RepairRequestedAt: diagnosticsTime(-DiagnosticRepairStallAfter - time.Minute),
				RepairStartedAt: diagnosticsTime(-2 * time.Hour),
				LastStartedAt:   diagnosticsTime(-30 * time.Second),
			},
			in:   DiagnosticInput{WorkerEnabled: true, Now: diagnosticsNow},
			want: CauseRepairStalled,
		},
		{
			// A repair is aged by its request time, so a recently requested one
			// stays protected even while its batches keep coming in.
			name: "repair still advancing",
			item: Diagnostics{
				CheckpointPresent: true, RepairColumnsPresent: true,
				RepairStatus: "RUNNING", RepairRequestedAt: diagnosticsTime(-time.Minute),
				RepairStartedAt: diagnosticsTime(-time.Minute),
				LastStartedAt:   diagnosticsTime(-30 * time.Second),
			},
			in:   DiagnosticInput{WorkerEnabled: true, Now: diagnosticsNow},
			want: CauseRepairRebuilding,
		},
		{
			name: "unfinished repair holds the reader even when the watermark is caught up",
			item: func() Diagnostics {
				item := healthy
				item.RepairStatus, item.RepairPhase = "RUNNING", "ROLLUP"
				item.RepairRequestedAt = diagnosticsTime(-time.Minute)
				item.RepairStartedAt = diagnosticsTime(-time.Minute)
				item.LastStartedAt = diagnosticsTime(-10 * time.Second)
				return item
			}(),
			in:   DiagnosticInput{WorkerEnabled: true, Now: diagnosticsNow},
			want: CauseRepairRebuilding,
		},
		{
			name: "behind with a worker that never committed a batch",
			item: Diagnostics{
				CheckpointPresent: true, RepairColumnsPresent: true,
				LastLogID: 0, NewestLogID: 1_809_129,
			},
			in:   DiagnosticInput{WorkerEnabled: true, Now: diagnosticsNow},
			want: CauseWorkerNeverRan,
		},
		{
			name: "backlog with a dead worker",
			item: Diagnostics{
				CheckpointPresent: true, RepairColumnsPresent: true,
				LastLogID: 848, LastCompletedAt: diagnosticsTime(-DiagnosticWorkerStallAfter - time.Minute),
				NewestLogID: 1_809_129,
			},
			in:   DiagnosticInput{WorkerEnabled: true, Now: diagnosticsNow},
			want: CauseWorkerStalled,
		},
		{
			name: "backlog with a live worker",
			item: Diagnostics{
				CheckpointPresent: true, RepairColumnsPresent: true,
				LastLogID: 848, LastCompletedAt: diagnosticsTime(-time.Minute),
				NewestLogID: 1_809_129,
			},
			in:   DiagnosticInput{WorkerEnabled: true, Now: diagnosticsNow},
			want: CauseRollupBacklog,
		},
		{
			// Retrying every tick but never committing, with the failure text
			// recorded: ordinary catch-up and a failing pipeline must not share
			// a verdict.
			name: "behind with failing batches",
			item: Diagnostics{
				CheckpointPresent: true, RepairColumnsPresent: true,
				LastLogID: 848, LastCompletedAt: diagnosticsTime(-3 * time.Hour),
				LastStartedAt: diagnosticsTime(-5 * time.Second),
				LastError:     "upsert analytics rollup: Error 1 (HY000): disk full",
				NewestLogID:   1_809_129,
			},
			in:   DiagnosticInput{WorkerEnabled: true, Now: diagnosticsNow},
			want: CauseRollupFailing,
		},
		{
			// Callers that do not track time must not receive age-based verdicts.
			name: "no clock disables staleness and falls back to backlog",
			item: Diagnostics{
				CheckpointPresent: true, RepairColumnsPresent: true,
				LastLogID: 848, LastCompletedAt: diagnosticsTime(-6 * time.Hour),
				NewestLogID: 1_809_129,
			},
			in:   DiagnosticInput{WorkerEnabled: true},
			want: CauseRollupBacklog,
		},
		{
			// Callers that do not track time must not age a repair out either.
			name: "no clock keeps an unfinished repair protected",
			item: Diagnostics{
				CheckpointPresent: true, RepairColumnsPresent: true,
				RepairStatus: "RUNNING", RepairRequestedAt: diagnosticsTime(-90 * time.Minute),
				RepairStartedAt: diagnosticsTime(-90 * time.Minute),
				LastStartedAt:   diagnosticsTime(-90 * time.Minute),
			},
			in:   DiagnosticInput{WorkerEnabled: true},
			want: CauseRepairRebuilding,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.item.Diagnose(tc.in); got != tc.want {
				t.Fatalf("Diagnose() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestDiagnosticsReproducesThe20261013ProductionOutage pins the verdict for the
// exact durable state observed on the production host: last_started_at fresh
// because the worker retried every tick, last_log_id frozen at 848 because not
// one of those retries could commit, and last_error carrying the temp-file
// permission failure.
//
// The expected verdict is rollup_failing, not worker_stalled and not
// rollup_backlog. A verdict of "backlog" would read as "busy catching up, wait"
// — an instruction that would have kept the incident alive indefinitely — and
// "stalled" would point at the process rather than at the environment fault the
// worker had already recorded in last_error.
func TestDiagnosticsReproducesThe20261013ProductionOutage(t *testing.T) {
	item := Diagnostics{
		CheckpointPresent:    true,
		RepairColumnsPresent: true,
		LastLogID:            848,
		LastLogCreatedAt:     diagnosticsTime(-17*time.Hour - 46*time.Minute - 51*time.Second),
		LastBatchSize:        5000,
		LastStartedAt:        diagnosticsTime(-11 * time.Second),
		LastCompletedAt:      diagnosticsTime(-17*time.Hour - 46*time.Minute - 50*time.Second),
		LastError:            "upsert analytics rollup: Error 1 (HY000): Can't create/write to file '/tmp/MLfHF27E' (OS errno 13 - Permission denied)",
		RepairStatus:         "COMPLETE",
		RepairPhase:          "IDLE",
		NewestLogID:          1_809_129,
		NewestLogCreatedAt:   diagnosticsTime(-time.Minute),
	}
	if got, want := item.Diagnose(DiagnosticInput{WorkerEnabled: true, Now: diagnosticsNow}), CauseRollupFailing; got != want {
		t.Fatalf("Diagnose() = %q, want %q", got, want)
	}
	if !item.Behind() {
		t.Fatal("the frozen watermark was not reported as behind")
	}
	if got, want := item.LagSeconds(), int64(17*3600+45*60+51); got != want {
		t.Fatalf("LagSeconds() = %d, want %d", got, want)
	}
}
