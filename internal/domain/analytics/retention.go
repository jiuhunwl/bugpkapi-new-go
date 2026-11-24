package analytics

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"bugpk-api/internal/pkg/businessclock"
	"bugpk-api/internal/pkg/logpartition"
)

// The five-minute risk contributor table is an input to detection, not a
// long-lived projection. Detection only ever reads the most recent window
// (riskcontrol's 1440-minute maximum), aligned to five-minute buckets, so a
// bucket older than that window can never change a verdict.
//
// Retention is a hard bound owned by the worker rather than "however long the
// operator cleanup job happens to leave behind", because the rollup consistency
// probe compares this table against the same window. If the two disagree the
// table is either unbounded or permanently reported as incomplete and rebuilt
// on every probe. Keeping the bound in one constant is what keeps them in step.
//
// 26 hours keeps a full 1440-minute window intact even immediately after a
// sweep, with slack for sweep jitter and clock skew.
const riskFingerprintRetention = 26 * time.Hour

// Sweeps are deliberately slow and bounded: they are housekeeping on a table
// that is written continuously, so they must never become the reason the worker
// cannot keep up with live traffic.
const (
	riskFingerprintPruneInterval   = 10 * time.Minute
	riskFingerprintPruneBatch      = 20_000
	riskFingerprintPruneMaxBatches = 20
	riskFingerprintPruneTimeout    = 30 * time.Second
)

// riskFingerprintWindowSQL is the single SQL form of the retention bound.
//
// The probe reads it for both the projection and its source so the two
// comparisons can never be made over different windows, and the sweep reuses it
// so retention and coverage are literally the same expression.
var riskFingerprintWindowSQL = fmt.Sprintf(
	"date_sub(now(), interval %d hour)",
	int(riskFingerprintRetention/time.Hour),
)

// Legacy day repairs cover the entire three-day recoverable log window.
// Resumable day repairs bind the frozen dataset floor instead, so crossing
// midnight never moves their clear/replay boundary. All daily tables retain
// history older than the source and it must never be cleared without replay.
const dayLayerRebuildWindowSQL = "date_sub(current_date(), interval 3 day)"

// PruneRiskFingerprint drops contributor buckets past the retention bound in
// bounded batches so a long-neglected table is trimmed over several sweeps
// instead of one long delete that would hold locks and upset live writes.
//
// The delete is a leading-primary-key range because bucket_start is the first
// column of the table's primary key, and it is idempotent: re-running it either
// removes nothing or removes exactly the buckets that are still too old.
func (r *Repository) PruneRiskFingerprint(ctx context.Context) (int64, error) {
	if r == nil || r.db == nil {
		return 0, errors.New("analytics repository database is nil")
	}
	var deleted int64
	for range riskFingerprintPruneMaxBatches {
		affected, err := r.deleteRetentionBatch(ctx, `
			delete from bp_call_risk_fingerprint_5m
			where bucket_start < `+riskFingerprintWindowSQL+`
			limit ?
		`, riskFingerprintPruneBatch)
		if err != nil {
			return deleted, fmt.Errorf("prune risk fingerprint buckets: %w", err)
		}
		deleted += affected
		if affected < int64(riskFingerprintPruneBatch) {
			return deleted, nil
		}
	}
	return deleted, nil
}

// pruneRiskFingerprintWithTimeout isolates a sweep from the rollup loop: a slow
// or failing delete records an error and returns, and the next tick continues
// aggregating.
func (r *Repository) pruneRiskFingerprintWithTimeout(ctx context.Context) {
	pruneCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), riskFingerprintPruneTimeout)
	defer cancel()
	if _, err := r.PruneRiskFingerprint(pruneCtx); err != nil && !errors.Is(err, context.Canceled) {
		_ = r.RecordError(pruneCtx, err)
	}
}

// ---------------------------------------------------------------------------
// Call-log dataset retention
//
// The retired nightly cron truncated bp_interface_call_log and every
// log-derived projection once a day, which kept the working set tiny but
// destroyed all history (and, before the guard fix, rewound the worker
// watermark). With the cron gone the tables would grow without bound, so the
// worker now owns the same housekeeping as bounded, idempotent sweeps.
//
// Two invariants drive the whole design:
//
//  1. The dataset floor is the call log's oldest surviving day. The coverage
//     probe compares minute-layer totals, day-layer totals and the raw log over
//     that floor, so EVERY pruned projection must retain at least as much as
//     the log. Pruning the minute layers with the same day-aligned bound as the
//     log keeps the two sides of every comparison identical; pruning them less
//     aggressively is also safe (extra old rows fall outside the floored
//     window), while pruning them more aggressively would flap the probe into
//     rebuilding the pruned range on every audit.
//  2. The log is pruned FIRST in every sweep. Deleting log rows advances the
//     floor; projections that still hold older rows only carry extra history
//     outside the floored window, which every comparison already excludes. The
//     reverse order would leave minute totals behind a floor the log still
//     supports and fabricate a drift signal.
//
// All bounds are day-aligned (`>= date_sub(current_date(), interval N day)`)
// so the floor, the projections and the prune bound always name the same
// calendar boundary regardless of sweep timing.
// ---------------------------------------------------------------------------

// callLogRetentionDays bounds the raw call log. Two days is the hard minimum:
// the day-layer rebuild window reaches back to yesterday 00:00, which at
// 23:59 is nearly 48 hours of log. Three days adds a day of slack and leaves
// users three days of call history (the retired cron kept roughly one).
const callLogRetentionDays = 3

// minuteLayerRetentionDays must equal callLogRetentionDays; see invariant 1.
const minuteLayerRetentionDays = callLogRetentionDays

// hourLayerRetentionDays bounds the four log-derived hour projections. Their
// widest reader is the 30-day dashboard region ranking, and the shared repair
// window is 31 days, so 31 keeps every served window intact without carrying
// the full 45-day layer contract on this box.
const hourLayerRetentionDays = 31

// billingRecordRetentionDays bounds settled billing rows. Only rows whose
// revenue amount is already materialized in the projection (or that were
// released without a charge) are deletable; RESERVED rows belong to the
// reconciliation loop and are never swept. The retired cron truncated this
// table nightly, so a 7-day settled retention is strictly more generous.
const billingRecordRetentionDays = 7

const (
	datasetSweepInterval   = 30 * time.Minute
	datasetSweepBatch      = 5_000
	datasetSweepMaxBatches = 40
	datasetSweepTimeout    = 60 * time.Second
)

// datasetSweepTarget is one table owned by the retention sweep, in mandatory
// sweep order: the log first, then the projections it feeds, then billing.
type datasetSweepTarget struct {
	name          string
	timeColumn    string
	retentionDays int
	// extraPredicate narrows the delete below the time bound (billing only).
	// It must keep the bound sargable on a time-leading index.
	extraPredicate string
}

var datasetSweepTargets = []datasetSweepTarget{
	{name: "bp_interface_call_log", timeColumn: "created_at", retentionDays: callLogRetentionDays},
	// The body side table shares the log's retention bound and is swept
	// immediately after the log (the log remains the floor-setter). It carries
	// a redundant created_at so it can be pruned on the same day-aligned bound
	// without a join back to the main table.
	{name: "bp_interface_call_log_body", timeColumn: "created_at", retentionDays: callLogRetentionDays},
	// The three minute layers must prune with the log's bound (invariant 1).
	{name: "bp_call_metric_minute", timeColumn: "bucket_start", retentionDays: minuteLayerRetentionDays},
	{name: "bp_call_api_metric_minute", timeColumn: "bucket_start", retentionDays: minuteLayerRetentionDays},
	{name: "bp_call_user_metric_minute", timeColumn: "bucket_start", retentionDays: minuteLayerRetentionDays},
	{name: "bp_call_region_ip_hour", timeColumn: "bucket_start", retentionDays: hourLayerRetentionDays},
	{name: "bp_call_region_hour", timeColumn: "bucket_start", retentionDays: hourLayerRetentionDays},
	{name: "bp_call_failure_ip_hour", timeColumn: "bucket_start", retentionDays: hourLayerRetentionDays},
	{name: "bp_call_failure_user_hour", timeColumn: "bucket_start", retentionDays: hourLayerRetentionDays},
	{
		name:          "bp_billing_record",
		timeColumn:    "created_at",
		retentionDays: billingRecordRetentionDays,
		extraPredicate: "(" +
			"(status='CONFIRMED' and revenue_projected=1) or status='RELEASED'" +
			") and ",
	},
}

// datasetSweepLogTable is the floor-setter; a failed log prune aborts the rest
// of the sweep (invariant 2).
const datasetSweepLogTable = "bp_interface_call_log"

func retentionDayBoundSQL(days int) string {
	return fmt.Sprintf("date_sub(current_date(), interval %d day)", days)
}

// SweepDatasetRetention trims the call log and every log-derived table past
// their retention bounds in bounded, idempotent batches. It never touches the
// day layers (they outlive the log by design), the risk fingerprint table (it
// has its own sweep above) or any RESERVED billing row.
func (r *Repository) SweepDatasetRetention(ctx context.Context) (int64, error) {
	if r == nil || r.db == nil {
		return 0, errors.New("analytics repository database is nil")
	}
	var deleted int64
	var sweepErrors error
	for _, target := range datasetSweepTargets {
		var affected int64
		var err error
		if target.name == datasetSweepLogTable {
			// The log is pruned first (invariant 2). When the table is
			// partitioned, fully-expired calendar months are dropped in one
			// metadata operation instead of a row-by-row delete, so cleanup
			// never holds row locks or emits redo/undo for an entire month of
			// history. The bounded delete still handles the current month's
			// partial window below.
			affected, err = r.pruneCallLog(ctx, target)
		} else {
			affected, err = r.pruneSweepTarget(ctx, target)
		}
		deleted += affected
		if err != nil {
			if target.name == datasetSweepLogTable {
				// Aborting here keeps the sweep safe: surviving projections
				// only ever carry extra pre-floor rows, while pruning them
				// past an unpruned log would fabricate drift.
				return deleted, fmt.Errorf("prune %s: %w", target.name, err)
			}
			sweepErrors = errors.Join(sweepErrors, fmt.Errorf("prune %s: %w", target.name, err))
			continue
		}
	}
	return deleted, sweepErrors
}

// pruneCallLog drops fully-expired monthly partitions (when the table is
// partitioned and the hook is wired) and then trims the current month's
// partial window with the bounded row delete. It keeps the two retention
// invariants: the log is pruned first, and every dropped partition's rows are
// all at or below the rollup watermark so no un-summarized tail or repair
// source is ever erased.
func (r *Repository) pruneCallLog(ctx context.Context, target datasetSweepTarget) (int64, error) {
	var deleted int64
	if r.dropExpiredPartitions != nil {
		dropped, err := r.dropExpiredPartitions(ctx, target.retentionDays)
		if err != nil {
			// A partition drop failure must not silently fall through to a
			// delete that would leave the expired month behind forever; surface
			// it and let the sweep abort the log phase (invariant 2 keeps
			// projections safe).
			return dropped, err
		}
		deleted += dropped
	}
	trimmed, err := r.pruneSweepTarget(ctx, target)
	return deleted + trimmed, err
}

// dropExpiredCallLogPartitions drops monthly partitions whose entire month is
// older than the retention window and whose ids are all below the rollup
// watermark. It is a no-op when the table is not partitioned or when no month
// qualifies, so installations without partitioning keep the bounded delete as
// their only cleanup path.
func (r *Repository) dropExpiredCallLogPartitions(ctx context.Context, retainDays int) (int64, error) {
	watermark, err := r.loadCallLogWatermark(ctx)
	if err != nil {
		return 0, err
	}
	plan, err := logpartition.DropExpired(ctx, r.db, logpartition.DropOptions{
		Now:        time.Now().In(businessclock.Location()),
		RetainDays: retainDays,
		MaxLogID:   watermark,
		Apply:      true,
	})
	if err != nil {
		return 0, err
	}
	return int64(len(plan.Partitions)), nil
}

// loadCallLogWatermark returns the rollup worker's committed high watermark,
// or zero when the checkpoint row is absent. Dropping a partition is only safe
// when every id inside it is <= this watermark, mirroring the delete guard in
// pruneSweepTarget.
func (r *Repository) loadCallLogWatermark(ctx context.Context) (int64, error) {
	var watermark int64
	err := r.db.QueryRowContext(ctx, `
		select coalesce(last_log_id, 0)
		from bp_analytics_checkpoint
		where worker_key='call_log_rollup'
		  and coalesce(repair_status,'NONE') not in ('PENDING','RUNNING')
		  and coalesce(repair_phase,'IDLE') not in ('CLEAR','ROLLUP','FINALIZE')
	`).Scan(&watermark)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil
		}
		return 0, err
	}
	return watermark, nil
}

func (r *Repository) pruneSweepTarget(ctx context.Context, target datasetSweepTarget) (int64, error) {
	query := "delete from " + target.name +
		" where " + target.extraPredicate + target.timeColumn +
		" < " + retentionDayBoundSQL(target.retentionDays) +
		" limit ?"
	if target.name == datasetSweepLogTable {
		// No time-only delete may erase a pending ingest tail or repair source.
		query = "delete from " + target.name + " where id <= coalesce((select last_log_id from bp_analytics_checkpoint where worker_key='call_log_rollup' and coalesce(repair_status,'NONE') not in ('PENDING','RUNNING') and coalesce(repair_phase,'IDLE') not in ('CLEAR','ROLLUP','FINALIZE')),0) and " + target.timeColumn + " < " + retentionDayBoundSQL(target.retentionDays) + " limit ?"
	}
	if target.retentionDays == minuteLayerRetentionDays && target.name != datasetSweepLogTable {
		// A repair can preserve older source past midnight. Never prune minute
		// contributions ahead of that dataset floor, or every audit would rebuild
		// the protected day forever. Suppress the delete throughout active repair.
		query = "delete from " + target.name + " where " + target.timeColumn + " < least(" + retentionDayBoundSQL(target.retentionDays) + ",coalesce((select cast(min(created_at) as date) from bp_interface_call_log)," + retentionDayBoundSQL(target.retentionDays) + ")) and exists(select 1 from bp_analytics_checkpoint where worker_key='call_log_rollup' and coalesce(repair_status,'NONE') not in ('PENDING','RUNNING') and coalesce(repair_phase,'IDLE') not in ('CLEAR','ROLLUP','FINALIZE')) limit ?"
	}
	var deleted int64
	for range datasetSweepMaxBatches {
		affected, err := r.deleteRetentionBatch(ctx, query, datasetSweepBatch)
		if err != nil {
			return deleted, err
		}
		deleted += affected
		if affected < int64(datasetSweepBatch) {
			return deleted, nil
		}
	}
	return deleted, nil
}

// Each delete commits independently under READ COMMITTED. InnoDB's default
// REPEATABLE READ range deletes can retain gap locks even when no row expires:
// the billing status ranges then block insertion of new RESERVED records.
// No consistent multi-statement snapshot is needed for idempotent retention.
func (r *Repository) deleteRetentionBatch(ctx context.Context, query string, args ...any) (int64, error) {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return affected, nil
}

// sweepDatasetRetentionWithTimeout isolates a sweep from the rollup loop the
// same way the risk prune is isolated: a slow or failing sweep records an
// error and returns, and the next tick continues aggregating.
func (r *Repository) sweepDatasetRetentionWithTimeout(ctx context.Context) {
	sweepCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), datasetSweepTimeout)
	defer cancel()
	if _, err := r.SweepDatasetRetention(sweepCtx); err != nil && !errors.Is(err, context.Canceled) {
		_ = r.RecordError(sweepCtx, err)
	}
}
