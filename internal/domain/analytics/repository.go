// Package analytics incrementally rolls the append-only gateway call log into
// bounded, index-friendly time buckets. It deliberately owns no HTTP routes;
// dashboard repositories may consume the tables independently.
package analytics

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"go.uber.org/zap"
	"sort"
	"strings"
	"sync"
	"time"

	"bugpk-api/internal/pkg/calloutcome"
	"bugpk-api/internal/pkg/iphash"
	"bugpk-api/internal/pkg/masking"
)

const (
	CheckpointKey                  = "call_log_rollup"
	DefaultBatchSize               = 2_000
	MaxBatchSize                   = 20_000
	rollupConsistencyProbeInterval = time.Minute
)

// optionalRollupProjections maps a rollup statement index to the table whose
// absence is tolerated while a database is still upgrading. Migration 000089
// introduced bp_call_api_metric_day and migration 000100 the day layer; in both
// cases an older schema (MIGRATION_ENABLED=false, or an upgrade whose SQL step
// failed) may keep serving traffic, so those projections are skipped instead of
// failing every batch and freezing the checkpoint. Every other projection is
// required and must surface its database errors.
//
// Keeping this as a map rather than a positional comparison is what lets the
// skip check and the "is the table missing" check agree on the same table name
// for every optional projection.
var optionalRollupProjections = map[int]string{
	projectionAPIDay:        "bp_call_api_metric_day",
	projectionMetricDay:     "bp_call_metric_day",
	projectionUserMetricDay: "bp_call_user_metric_day",
}

// optionalRollupTable returns the table a statement writes to when that
// projection may legitimately be absent.
func optionalRollupTable(index int) (string, bool) {
	table, ok := optionalRollupProjections[index]
	return table, ok
}

func isOptionalRollupStatement(index int) bool {
	_, ok := optionalRollupProjections[index]
	return ok
}

// optionalRollupTables lists the tolerant projections in a deterministic order
// so probes and tests never depend on Go's map iteration order.
func optionalRollupTables() []string {
	indexes := make([]int, 0, len(optionalRollupProjections))
	for index := range optionalRollupProjections {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	tables := make([]string, 0, len(indexes))
	for _, index := range indexes {
		tables = append(tables, optionalRollupProjections[index])
	}
	return tables
}

func isMissingOptionalRollupTable(err error, table string) bool {
	if err == nil || table == "" {
		return false
	}
	message := strings.ToLower(err.Error())
	if !strings.Contains(message, strings.ToLower(table)) {
		return false
	}
	// MySQL reports a missing table as 1146/42S02. Keep this narrow so a
	// malformed query, missing column, or permission error is never hidden.
	return strings.Contains(message, "error 1146") ||
		strings.Contains(message, "42s02") ||
		(strings.Contains(message, "doesn't exist") && !strings.Contains(message, "key '"))
}

// rollupState describes the minimum information needed to decide whether a
// checkpoint can be safely replayed. Replaying is only safe when every
// projection is empty; otherwise resetting the checkpoint could double-count
// rows that are already present in one of the destination tables.
type rollupState struct {
	CheckpointID      int64
	NewestLogID       int64
	HasEligibleSource bool
	HasAnyRollup      bool
	RepairStatus      string
	RepairWindowDays  int64
	// WindowRepairMask identifies recent projections that are missing or
	// inconsistent while the checkpoint is already caught up.  It lets the
	// worker repair only the damaged projection instead of blindly replaying
	// the complete historical log.
	WindowRepairMask uint16
}

func isMissingRepairColumnError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	if !(strings.Contains(message, "error 1054") || strings.Contains(message, "42s22")) {
		return false
	}
	for _, column := range []string{
		"repair_status",
		"repair_window_days",
		"repair_requested_at",
		"repair_started_at",
		"repair_completed_at",
	} {
		if strings.Contains(message, column) {
			return true
		}
	}
	return false
}

func (s rollupState) needsReplay() bool {
	return s.CheckpointID > 0 &&
		s.NewestLogID > 0 &&
		s.HasEligibleSource &&
		!s.HasAnyRollup
}

var rollupProjectionTables = []string{
	"bp_call_metric_minute",
	"bp_call_api_metric_minute",
	"bp_call_api_metric_day",
	"bp_call_user_metric_minute",
	"bp_call_region_ip_hour",
	"bp_call_region_hour",
	"bp_call_failure_ip_hour",
	"bp_call_failure_user_hour",
	"bp_call_risk_fingerprint_5m",
	"bp_call_metric_day",
	"bp_call_user_metric_day",
}

var requiredRollupProjectionTables = []string{
	"bp_call_metric_minute",
	"bp_call_api_metric_minute",
	"bp_call_user_metric_minute",
	"bp_call_region_ip_hour",
	"bp_call_region_hour",
	"bp_call_failure_ip_hour",
	"bp_call_failure_user_hour",
	"bp_call_risk_fingerprint_5m",
}

// rollupConsistencyQuery uses bounded primary-key existence probes. Keep the
// complete projection list here: ProcessBatch replays all projections
// together, so a partial destination must never be mistaken for an empty one.
// The runtime probe uses requiredRollupConsistencyQuery plus the optional
// projection probe below so an older schema can continue processing core data.
func rollupConsistencyQuery() string {
	return rollupConsistencyQueryForTables(rollupProjectionTables)
}

func requiredRollupConsistencyQuery() string {
	return rollupConsistencyQueryForTables(requiredRollupProjectionTables)
}

func rollupConsistencyQueryForTables(tables []string) string {
	probes := make([]string, 0, len(tables))
	for _, table := range tables {
		probes = append(probes, "exists(select 1 from "+table+" limit 1)")
	}
	return "select\n\t\t" + strings.Join(probes, ",\n\t\t")
}

// optionalRollupConsistencyQuery probes a single tolerant projection. The table
// name is interpolated from optionalRollupProjections rather than duplicated, so
// adding a projection cannot leave the probe pointing at the wrong table.
func optionalRollupConsistencyQuery(table string) string {
	return "select exists(select 1 from " + table + " limit 1)"
}

const eligibleSourceExistsQuery = `
	select exists(
		select 1
		from bp_interface_call_log l
		where l.id<=?
		  and l.charge_type<>'ADMIN'
		  and exists(select 1 from bp_user u where u.id=l.user_id)
		limit 1
	)
`

func probeRollupState(ctx context.Context, tx *sql.Tx, checkpointID, newestLogID int64, includeSourceCoverage bool) (rollupState, error) {
	state := rollupState{CheckpointID: checkpointID, NewestLogID: newestLogID}
	if checkpointID < 0 || newestLogID <= 0 {
		return state, nil
	}

	flags := make([]int, len(requiredRollupProjectionTables))
	args := make([]any, len(flags))
	for i := range flags {
		args[i] = &flags[i]
	}
	if err := tx.QueryRowContext(ctx, requiredRollupConsistencyQuery()).Scan(args...); err != nil {
		return rollupState{}, fmt.Errorf("probe analytics rollup consistency: %w", err)
	}
	for _, flag := range flags {
		if flag != 0 {
			state.HasAnyRollup = true
			break
		}
	}

	// The optional projections are probed separately so an older schema can keep
	// processing the core projections while those migrations are being applied.
	for _, table := range optionalRollupTables() {
		var present int
		if err := tx.QueryRowContext(ctx, optionalRollupConsistencyQuery(table)).Scan(&present); err != nil {
			if !isMissingOptionalRollupTable(err, table) {
				return rollupState{}, fmt.Errorf("probe optional analytics rollup: %w", err)
			}
			continue
		}
		if present != 0 {
			state.HasAnyRollup = true
		}
	}

	var eligible int
	if err := tx.QueryRowContext(ctx, eligibleSourceExistsQuery, newestLogID).Scan(&eligible); err != nil {
		return rollupState{}, fmt.Errorf("probe eligible analytics source: %w", err)
	}
	state.HasEligibleSource = eligible != 0
	if checkpointID == newestLogID && state.HasEligibleSource {
		coverage, err := probeRecentRollupCoverage(ctx, tx, newestLogID, includeSourceCoverage)
		if err != nil {
			return rollupState{}, err
		}
		state.WindowRepairMask = coverage.repairMask()
	}
	return state, nil
}

// Checkpoint is the committed high-water mark. LastLogID advances over every
// source row, including ADMIN rows and rows whose user no longer exists, so a
// permanently ineligible row cannot stall processing.
type Checkpoint struct {
	WorkerKey        string
	LastLogID        int64
	LastLogCreatedAt sql.NullTime
	SourceRows       uint64
	ContributionRows uint64
	LastBatchSize    int
	LastStartedAt    sql.NullTime
	LastCompletedAt  sql.NullTime
	LastError        string
}

type BatchResult struct {
	FromLogID        int64
	ToLogID          int64
	SourceRows       int
	ContributionRows int
	HasMore          bool
}

type Repository struct {
	log                  *zap.Logger
	db                   *sql.DB
	consistencyProbeMu   sync.Mutex
	lastConsistencyProbe time.Time

	// dropExpiredPartitions drops fully-expired monthly partitions of the call
	// log. It is wired to the real logpartition.DropExpired in NewRepository;
	// tests that construct &Repository{db: ...} directly leave it nil, which
	// makes the retention sweep skip partition drops and use only the bounded
	// row delete. A nil hook is the "not partitioned" fast path, not an error.
	dropExpiredPartitions func(ctx context.Context, retainDays int) (int64, error)
}

func NewRepository(db *sql.DB) *Repository {
	r := &Repository{db: db}
	r.dropExpiredPartitions = r.dropExpiredCallLogPartitions
	return r
}

// consistencyProbeDue reports whether the in-process consistency check should
// run again. The check is deliberately low frequency: every batch already
// advances the checkpoint under a row lock, while probing nine projections on
// every batch adds avoidable metadata and index work during catch-up.
func (r *Repository) consistencyProbeDue(now time.Time) bool {
	if r == nil {
		return false
	}
	r.consistencyProbeMu.Lock()
	defer r.consistencyProbeMu.Unlock()
	return r.consistencyProbeDueLocked(now)
}

func (r *Repository) consistencyProbeDueLocked(now time.Time) bool {
	return r.lastConsistencyProbe.IsZero() || now.Sub(r.lastConsistencyProbe) >= rollupConsistencyProbeInterval
}

// Source coverage is a deliberately rarer audit than the cheap projection
// existence/watermark probe. It scans only the bounded recovery window and is
// needed primarily once after a deployment/restore; the durable 000090 task
// handles subsequent migrations without repeating this raw-log aggregate.
const rollupSourceCoverageProbeInterval = 24 * time.Hour

func (r *Repository) invalidateConsistencyProbe() {
	if r == nil {
		return
	}
	r.consistencyProbeMu.Lock()
	r.lastConsistencyProbe = time.Time{}
	r.consistencyProbeMu.Unlock()
}

// probeRollupStateIfDue serializes the probe within one process and records its
// timestamp only after a successful query. A failed probe is retried on the
// next batch, which is important while an optional migration table is being
// created or a connection is briefly unavailable.
func (r *Repository) probeRollupStateIfDue(
	ctx context.Context,
	tx *sql.Tx,
	checkpointID int64,
	newestLogID int64,
) (rollupState, bool, error) {
	state := rollupState{CheckpointID: checkpointID, NewestLogID: newestLogID}
	if r == nil || checkpointID < 0 || newestLogID <= 0 {
		return state, false, nil
	}

	r.consistencyProbeMu.Lock()
	defer r.consistencyProbeMu.Unlock()
	// A consistency probe is useful at the initial checkpoint or after the
	// source backlog is drained. Probing every batch while catching up adds
	// nine metadata/index probes without improving the recovery decision; the
	// final drained batch invalidates the timestamp below so the next tick
	// performs the caught-up probe immediately.
	if checkpointID != 0 && checkpointID != newestLogID {
		return state, false, nil
	}
	now := time.Now()
	if !r.consistencyProbeDueLocked(now) {
		return state, false, nil
	}
	// Full source audits run on a separate bounded read-only connection, never
	// while ingestion holds the checkpoint row lock.
	state, err := probeRollupState(ctx, tx, checkpointID, newestLogID, false)
	if err != nil {
		return state, true, err
	}
	r.lastConsistencyProbe = now
	return state, true, nil
}

// Checkpoint returns the durable rollup position.
func (r *Repository) Checkpoint(ctx context.Context) (Checkpoint, error) {
	if r == nil || r.db == nil {
		return Checkpoint{}, errors.New("analytics repository database is nil")
	}
	var item Checkpoint
	err := r.db.QueryRowContext(ctx, `
		select worker_key,last_log_id,last_log_created_at,source_rows,contribution_rows,
		       last_batch_size,last_started_at,last_completed_at,last_error
		from bp_analytics_checkpoint where worker_key=?
	`, CheckpointKey).Scan(
		&item.WorkerKey,
		&item.LastLogID,
		&item.LastLogCreatedAt,
		&item.SourceRows,
		&item.ContributionRows,
		&item.LastBatchSize,
		&item.LastStartedAt,
		&item.LastCompletedAt,
		&item.LastError,
	)
	return item, err
}

// ProcessBatch atomically rolls up the next primary-key range. A checkpoint
// row lock serializes multiple processes. Every destination upsert and the
// checkpoint update share one transaction, making retry after a crash
// idempotent: either the whole range commits exactly once, or none of it does.
func (r *Repository) ProcessBatch(ctx context.Context, batchSize int) (BatchResult, error) {
	if r == nil || r.db == nil {
		return BatchResult{}, errors.New("analytics repository database is nil")
	}
	batchSize = normalizeBatchSize(batchSize)
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return BatchResult{}, fmt.Errorf("begin analytics batch: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `
		insert ignore into bp_analytics_checkpoint(worker_key) values (?)
	`, CheckpointKey); err != nil {
		return BatchResult{}, fmt.Errorf("initialize analytics checkpoint: %w", err)
	}

	var fromID int64
	var repairStatus string
	var repairWindowDays int64
	var repairProjectionMask uint64
	repairCursor := repairCursorState{}
	repairColumnsAvailable := true
	repairCursorColumnsAvailable := true
	lockErr := tx.QueryRowContext(ctx, `
		select last_log_id,coalesce(repair_status,''),coalesce(repair_window_days,0),
		       coalesce(repair_upper_log_id,0),coalesce(repair_cursor_id,0),
		       coalesce(repair_projection_mask,0),coalesce(repair_phase,''),
		       repair_window_start,coalesce(repair_clear_projection,0)
		from bp_analytics_checkpoint where worker_key=? for update
	`, CheckpointKey).Scan(
		&fromID,
		&repairStatus,
		&repairWindowDays,
		&repairCursor.UpperLogID,
		&repairCursor.CursorID,
		&repairProjectionMask,
		&repairCursor.Phase,
		&repairCursor.WindowStart,
		&repairCursor.ClearProjection,
	)
	if lockErr != nil && isMissingRepairCursorColumnError(lockErr) {
		repairCursorColumnsAvailable = false
		lockErr = tx.QueryRowContext(ctx, `
			select last_log_id,coalesce(repair_status,''),coalesce(repair_window_days,0)
			from bp_analytics_checkpoint where worker_key=? for update
		`, CheckpointKey).Scan(&fromID, &repairStatus, &repairWindowDays)
	}
	if lockErr != nil && isMissingRepairColumnError(lockErr) {
		// Keep MIGRATION_ENABLED=false / rolling-upgrade deployments operational
		// until 000090 has been applied. The durable repair task is simply not
		// available on that older schema; the existing checkpoint heuristics still
		// protect the core projections.
		repairColumnsAvailable = false
		repairCursorColumnsAvailable = false
		lockErr = tx.QueryRowContext(ctx, `
			select last_log_id from bp_analytics_checkpoint where worker_key=? for update
		`, CheckpointKey).Scan(&fromID)
	}
	if lockErr != nil {
		return BatchResult{}, fmt.Errorf("lock analytics checkpoint: %w", lockErr)
	}
	if repairCursorColumnsAvailable {
		repairCursor.Available = true
		repairCursor.ProjectionMask = uint16(repairProjectionMask & uint64(allProjectionRepairMask()))
	}
	if _, err := tx.ExecContext(ctx, `
		update bp_analytics_checkpoint set last_started_at=now(),last_error='' where worker_key=?
	`, CheckpointKey); err != nil {
		return BatchResult{}, fmt.Errorf("mark analytics batch started: %w", err)
	}

	// Guard against a watermark that points past the newest log row. This
	// happens when a dataset is restored or migrated while the checkpoint
	// table keeps ids from the previous dataset: every tick then finds zero
	// source rows forever while real traffic keeps appending rows below the
	// stale watermark, so every rollup stays permanently empty even though
	// the worker runs. Rewind the watermark to the newest existing id; the
	// ids in between do not exist in this dataset, so nothing is skipped and
	// nothing is aggregated twice. The max(id) probe is a covering index-tail
	// read and stays off the hot path.
	var newestLogID int64
	var newestLogCreatedAt sql.NullTime
	if err := tx.QueryRowContext(ctx, `
		select coalesce(max(id),0),max(created_at) from bp_interface_call_log
	`).Scan(&newestLogID, &newestLogCreatedAt); err != nil {
		return BatchResult{}, fmt.Errorf("probe analytics log watermark: %w", err)
	}
	// Rewind a watermark left behind by a restore/import before checking
	// projection coverage. The following probe can then repair a partially
	// populated recent window without replaying the whole history.
	if newestLogID < fromID {
		if _, err := tx.ExecContext(ctx, `
			update bp_analytics_checkpoint
			set last_log_id=?,last_log_created_at=null,last_batch_size=0
			where worker_key=?
		`, newestLogID, CheckpointKey); err != nil {
			return BatchResult{}, fmt.Errorf("rewind stale analytics checkpoint: %w", err)
		}
		fromID = newestLogID
	}

	// Migration 000090 persists this task before the worker starts.  Handle it
	// before the low-frequency consistency probe: a pending task must not wait
	// for the probe interval, and uniform omissions have identical totals and
	// watermarks that the heuristic probe cannot detect.
	if repairColumnsAvailable && (rollupState{RepairStatus: repairStatus}).needsPersistedRepair() {
		if repairCursorColumnsAvailable {
			result, err := r.processResumableRepairStep(
				ctx,
				tx,
				repairCursor,
				newestLogID,
				repairWindowDays,
				batchSize,
			)
			if err != nil {
				r.invalidateConsistencyProbe()

				return BatchResult{}, err
			}
			if err := tx.Commit(); err != nil {
				r.invalidateConsistencyProbe()

				return BatchResult{}, fmt.Errorf("commit resumable analytics repair step: %w", err)
			}
			return result, nil
		}
		if _, err := tx.ExecContext(ctx, `
			update bp_analytics_checkpoint
			set repair_status=?,repair_started_at=now(6),last_error=''
			where worker_key=?
		`, repairStatusRunning, CheckpointKey); err != nil {
			return BatchResult{}, fmt.Errorf("mark analytics repair started: %w", err)
		}
		if err := r.repairRecentProjections(
			ctx,
			tx,
			allProjectionRepairMask(),
			newestLogID,
			repairWindowDays,
		); err != nil {
			r.invalidateConsistencyProbe()

			return BatchResult{}, err
		}
		if _, err := tx.ExecContext(ctx, `
			update bp_analytics_checkpoint
			set last_log_id=?,last_log_created_at=?,last_batch_size=0,last_completed_at=now(6),
			    last_error='',repair_status=?,repair_completed_at=now(6)
			where worker_key=?
		`, newestLogID, newestLogCreatedAt, repairStatusComplete, CheckpointKey); err != nil {
			r.invalidateConsistencyProbe()

			return BatchResult{}, fmt.Errorf("mark analytics repair complete: %w", err)
		}
		if err := tx.Commit(); err != nil {
			r.invalidateConsistencyProbe()

			return BatchResult{}, fmt.Errorf("commit analytics projection repair: %w", err)
		}
		return BatchResult{FromLogID: fromID, ToLogID: newestLogID}, nil
	}

	// A migration/import can preserve a caught-up checkpoint while leaving
	// only one dashboard projection populated. Probe and repair only when
	// there is no ordinary source backlog; this keeps recovery serialized by
	// the checkpoint row lock and avoids double counting existing buckets.
	state, probed, err := r.probeRollupStateIfDue(ctx, tx, fromID, newestLogID)
	if err != nil {
		return BatchResult{}, err
	}
	state.RepairStatus = repairStatus
	state.RepairWindowDays = repairWindowDays
	if probed && state.needsWindowRepair() {
		if repairCursorColumnsAvailable {
			result, err := r.scheduleResumableRepair(
				ctx,
				tx,
				state.WindowRepairMask,
				newestLogID,
				rollupRepairWindowDays,
			)
			if err != nil {
				r.invalidateConsistencyProbe()

				return BatchResult{}, err
			}
			if err := tx.Commit(); err != nil {
				r.invalidateConsistencyProbe()

				return BatchResult{}, fmt.Errorf("commit analytics repair schedule: %w", err)
			}
			return result, nil
		}
		if err := r.repairRecentProjections(ctx, tx, state.WindowRepairMask, newestLogID, rollupRepairWindowDays); err != nil {
			r.invalidateConsistencyProbe()

			return BatchResult{}, err
		}
		if _, err := tx.ExecContext(ctx, `
			update bp_analytics_checkpoint
			set last_log_id=?,last_batch_size=0,last_completed_at=now(),last_error=''
			where worker_key=?
		`, newestLogID, CheckpointKey); err != nil {
			r.invalidateConsistencyProbe()

			return BatchResult{}, fmt.Errorf("mark repaired analytics checkpoint: %w", err)
		}
		if err := tx.Commit(); err != nil {
			r.invalidateConsistencyProbe()

			return BatchResult{}, fmt.Errorf("commit analytics projection repair: %w", err)
		}
		return BatchResult{FromLogID: newestLogID, ToLogID: newestLogID}, nil
	}
	// A restored/imported database can have a zero checkpoint together with
	// pre-populated rollups (for example, a migration backfilled one projection
	// before the checkpoint row was restored). Replaying from id 0 would add the
	// same source rows a second time. Replace only the bounded dashboard window
	// and advance the watermark; historical rows already present in the
	// projections are left untouched.
	if probed && state.needsCheckpointZeroRepair() {
		if repairCursorColumnsAvailable {
			result, err := r.scheduleResumableRepair(
				ctx,
				tx,
				allProjectionRepairMask(),
				newestLogID,
				rollupRepairWindowDays,
			)
			if err != nil {
				r.invalidateConsistencyProbe()

				return BatchResult{}, err
			}
			if err := tx.Commit(); err != nil {
				r.invalidateConsistencyProbe()

				return BatchResult{}, fmt.Errorf("commit zero-checkpoint analytics repair schedule: %w", err)
			}
			return result, nil
		}
		if err := r.repairRecentProjections(ctx, tx, allProjectionRepairMask(), newestLogID, rollupRepairWindowDays); err != nil {
			r.invalidateConsistencyProbe()

			return BatchResult{}, err
		}
		if _, err := tx.ExecContext(ctx, `
			update bp_analytics_checkpoint
			set last_log_id=?,last_batch_size=0,last_completed_at=now(),last_error=''
			where worker_key=?
		`, newestLogID, CheckpointKey); err != nil {
			r.invalidateConsistencyProbe()

			return BatchResult{}, fmt.Errorf("mark zero-checkpoint analytics repair: %w", err)
		}
		if err := tx.Commit(); err != nil {
			r.invalidateConsistencyProbe()

			return BatchResult{}, fmt.Errorf("commit zero-checkpoint analytics repair: %w", err)
		}
		return BatchResult{FromLogID: 0, ToLogID: newestLogID}, nil
	}
	if probed && state.needsReplay() {
		if _, err := tx.ExecContext(ctx, `
			update bp_analytics_checkpoint
			set last_log_id=0,last_log_created_at=null,source_rows=0,
			    contribution_rows=0,last_batch_size=0
			where worker_key=?
		`, CheckpointKey); err != nil {
			return BatchResult{}, fmt.Errorf("reset empty analytics rollup checkpoint: %w", err)
		}
		fromID = 0
	}

	var toID int64
	var sourceRows int
	var lastCreatedAt sql.NullTime
	err = tx.QueryRowContext(ctx, `
		select coalesce(max(id),0),count(*),max(created_at)
		from (
			select id,created_at
			from bp_interface_call_log
			where id>?
			order by id
			limit ?
		) source_window
	`, fromID, batchSize).Scan(&toID, &sourceRows, &lastCreatedAt)
	if err != nil {
		return BatchResult{}, fmt.Errorf("select analytics source range: %w", err)
	}
	if sourceRows == 0 {
		if _, err := tx.ExecContext(ctx, `
			update bp_analytics_checkpoint
			set last_batch_size=0,last_completed_at=now(),last_error=''
			where worker_key=?
		`, CheckpointKey); err != nil {
			return BatchResult{}, fmt.Errorf("mark empty analytics batch: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return BatchResult{}, fmt.Errorf("commit empty analytics batch: %w", err)
		}
		return BatchResult{FromLogID: fromID, ToLogID: fromID}, nil
	}

	for index, statement := range rollupStatements() {
		if _, err := tx.ExecContext(ctx, statement, fromID, toID); err != nil {
			if table, optional := optionalRollupTable(index); optional && isMissingOptionalRollupTable(err, table) {
				continue
			}
			return BatchResult{}, fmt.Errorf("upsert analytics rollup: %w", err)
		}
	}

	contributionRows, err := countContributions(ctx, tx, fromID, toID)
	if err != nil {
		return BatchResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		update bp_analytics_checkpoint
		set last_log_id=?,last_log_created_at=?,source_rows=source_rows+?,
		    contribution_rows=contribution_rows+?,last_batch_size=?,
		    last_completed_at=now(),last_error=''
		where worker_key=?
	`, toID, lastCreatedAt, sourceRows, contributionRows, sourceRows, CheckpointKey); err != nil {
		return BatchResult{}, fmt.Errorf("advance analytics checkpoint: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return BatchResult{}, fmt.Errorf("commit analytics batch: %w", err)
	}

	hasMore := sourceRows == batchSize
	if hasMore {
		var next int
		if err := r.db.QueryRowContext(ctx, `
			select exists(select 1 from bp_interface_call_log where id>? limit 1)
		`, toID).Scan(&next); err != nil {
			return BatchResult{}, fmt.Errorf("check analytics backlog: %w", err)
		}
		hasMore = next == 1
	}
	// If this batch drained a source backlog, force the next tick to perform a
	// caught-up consistency probe. This avoids recording a probe timestamp while
	// the checkpoint was behind the source high-water mark and then postponing
	// repair for a full interval after catch-up.
	if sourceRows > 0 && !hasMore {
		r.invalidateConsistencyProbe()
	}
	return BatchResult{
		FromLogID:        fromID,
		ToLogID:          toID,
		SourceRows:       sourceRows,
		ContributionRows: contributionRows,
		HasMore:          hasMore,
	}, nil
}

func normalizeBatchSize(size int) int {
	if size <= 0 {
		return DefaultBatchSize
	}
	if size > MaxBatchSize {
		return MaxBatchSize
	}
	return size
}

func countContributions(ctx context.Context, tx *sql.Tx, fromID, toID int64) (int, error) {
	var count int
	err := tx.QueryRowContext(ctx, `
		select count(*)
		from bp_interface_call_log l
		where l.id>? and l.id<=?
		  and l.charge_type<>'ADMIN'
		  and exists(select 1 from bp_user u where u.id=l.user_id)
	`, fromID, toID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count analytics contributions: %w", err)
	}
	return count, nil
}

// RecordError is best-effort observability for a failed worker iteration. It
// never changes the high-water mark.
func (r *Repository) RecordError(ctx context.Context, processErr error) error {
	if r == nil || r.db == nil || processErr == nil {
		return nil
	}
	message := masking.Error(processErr)
	if len(message) > 1000 {
		message = message[:1000]
	}
	_, err := r.db.ExecContext(ctx, `
		update bp_analytics_checkpoint set last_error=? where worker_key=?
	`, message, CheckpointKey)
	return err
}

// RunUntilCaughtUp processes at most maxBatches and returns promptly when the
// source is drained. maxBatches <= 0 means one batch, preventing accidental
// unbounded work in a request or startup path.
func (r *Repository) RunUntilCaughtUp(ctx context.Context, batchSize, maxBatches int) (BatchResult, error) {
	if maxBatches <= 0 {
		maxBatches = 1
	}
	var total BatchResult
	for range maxBatches {
		result, err := r.ProcessBatch(ctx, batchSize)
		if err != nil {
			return total, err
		}
		if total.SourceRows == 0 {
			total.FromLogID = result.FromLogID
		}
		total.ToLogID = result.ToLogID
		total.SourceRows += result.SourceRows
		total.ContributionRows += result.ContributionRows
		total.HasMore = result.HasMore
		if !result.HasMore {
			break
		}
	}
	return total, nil
}

// rollupStatements 每次调用返回全部 upsert。盐相关的三条语句按调用时的
// iphash 前缀动态拼装（Configure 在启动装配阶段、worker 启动前执行，之后
// 不再变化），因此不能做成包级 var 提前固化。
func rollupStatements() []string {
	return []string{
		overallMinuteUpsert,
		apiMinuteUpsert,
		apiDayUpsert,
		userMinuteUpsert,
		regionIPHourUpsert(),
		regionHourUpsert,
		failureIPHourUpsert,
		failureUserHourUpsert(),
		riskFingerprintUpsert(),
		// The day layer is appended last so every existing projection keeps its
		// index, and therefore its repair bit, unchanged. See the iota block in
		// repair.go.
		metricDayUpsert,
		userMetricDayUpsert,
	}
}

// metricDayUpsert is the coarse all-user projection behind the 7-day trend.
// It aggregates the same eligible rows as overallMinuteUpsert with the same
// arithmetic, so the two agree over any window even though this one costs one
// row per day instead of 1,440.
var metricDayUpsert = `
	insert into bp_call_metric_day(
		bucket_date,call_count,success_count,failure_count,billable_count,
		charge_amount,point_amount,request_bytes,response_bytes,elapsed_sum,
		elapsed_min,elapsed_max,first_seen,last_seen
	)
	select date(l.created_at),
	       count(*),sum(l.success=1),sum(` + statsFailureExpr + `),sum(l.billable=1),
	       sum(l.charge_amount),sum(l.point_price),sum(l.request_size),sum(l.response_size),
	       sum(l.elapsed_ms),min(l.elapsed_ms),max(l.elapsed_ms),min(l.created_at),max(l.created_at)
	from bp_interface_call_log l
	where ` + sourceEligibility + `
	group by date(l.created_at)
	on duplicate key update ` + metricAdditions

// userMetricDayUpsert keeps the user dimension because the account-status
// filter is a read-time join against bp_user and must reflect the current
// status rather than the status at aggregation time.
var userMetricDayUpsert = `
	insert into bp_call_user_metric_day(
		bucket_date,user_id,call_count,success_count,failure_count,billable_count,
		charge_amount,point_amount,request_bytes,response_bytes,elapsed_sum,
		elapsed_min,elapsed_max,first_seen,last_seen
	)
	select date(l.created_at),l.user_id,
	       count(*),sum(l.success=1),sum(` + statsFailureExpr + `),sum(l.billable=1),
	       sum(l.charge_amount),sum(l.point_price),sum(l.request_size),sum(l.response_size),
	       sum(l.elapsed_ms),min(l.elapsed_ms),max(l.elapsed_ms),min(l.created_at),max(l.created_at)
	from bp_interface_call_log l
	where ` + sourceEligibility + `
	group by date(l.created_at),l.user_id
	on duplicate key update ` + metricAdditions

const sourceEligibility = `
		l.id>? and l.id<=?
		and l.charge_type<>'ADMIN'
		and exists(select 1 from bp_user source_user where source_user.id=l.user_id)`

// statsFailureExpr keeps the v1.2.30 failure_count contract (a non-404
// failure), extended since 1.2.54 to also exclude expected-empty business 404
// rows (HTTP 2xx + {"code":404}, outcome_class=2).
//
// Coherence contract: every rollup projection here, the source coverage /
// watermark mirrors in repair.go, the per-row oracle in
// user/admin_call_snapshot.go, and the log-fallback queries in user/ and
// observability/ must use the exact same predicate, otherwise coverage probes
// will flag permanent drift. riskFingerprintUpsert is exempt: it feeds risk
// detection, not rate statistics, and its comparison is watermark-only.
//
// Both expressions come from the shared calloutcome SQL helpers so every rollup
// projection, probe, repair path and log fallback uses one definition. They are
// vars (not consts) because the helper builds the alias-qualified expression at
// init time. Changing the predicate is expected to disagree with projections
// aggregated under the old definition; the repair pipeline replays the retained
// log window (about a day) under the new one, and older durable day-layer rows
// keep their historical values by design.
var statsFailureExpr = calloutcome.SQLLegacyFailure("l")
var callOutcomeClassSQL = calloutcome.SQLClass("l")

const outcomeMinuteAdditions = `
	expected_empty_count=expected_empty_count+values(expected_empty_count),
	client_error_count=client_error_count+values(client_error_count),
	throttle_count=throttle_count+values(throttle_count),
	upstream_error_count=upstream_error_count+values(upstream_error_count)`

const metricAdditions = `
	call_count=call_count+values(call_count),
	success_count=success_count+values(success_count),
	failure_count=failure_count+values(failure_count),
	billable_count=billable_count+values(billable_count),
	charge_amount=charge_amount+values(charge_amount),
	point_amount=point_amount+values(point_amount),
	request_bytes=request_bytes+values(request_bytes),
	response_bytes=response_bytes+values(response_bytes),
	elapsed_sum=elapsed_sum+values(elapsed_sum),
	elapsed_min=least(elapsed_min,values(elapsed_min)),
	elapsed_max=greatest(elapsed_max,values(elapsed_max)),
	first_seen=least(first_seen,values(first_seen)),
	last_seen=greatest(last_seen,values(last_seen))`

var overallMinuteUpsert = `
	insert into bp_call_metric_minute(
		bucket_start,call_count,success_count,failure_count,billable_count,
		charge_amount,point_amount,request_bytes,response_bytes,elapsed_sum,
		elapsed_min,elapsed_max,first_seen,last_seen,
		expected_empty_count,client_error_count,throttle_count,upstream_error_count
	)
	select from_unixtime(floor(unix_timestamp(l.created_at)/60)*60),
	       count(*),sum(l.success=1),sum(` + statsFailureExpr + `),sum(l.billable=1),
	       sum(l.charge_amount),sum(l.point_price),sum(l.request_size),sum(l.response_size),
	       sum(l.elapsed_ms),min(l.elapsed_ms),max(l.elapsed_ms),min(l.created_at),max(l.created_at),
	       sum((` + callOutcomeClassSQL + `)=2),sum((` + callOutcomeClassSQL + `)=3),
	       sum((` + callOutcomeClassSQL + `)=4),sum((` + callOutcomeClassSQL + `)=5)
	from bp_interface_call_log l
	where ` + sourceEligibility + `
	group by from_unixtime(floor(unix_timestamp(l.created_at)/60)*60)
	on duplicate key update ` + metricAdditions + `,` + outcomeMinuteAdditions

var apiMinuteUpsert = `
	insert into bp_call_api_metric_minute(
		bucket_start,interface_id,user_id,api_code,call_count,success_count,failure_count,
		billable_count,charge_amount,point_amount,request_bytes,response_bytes,elapsed_sum,
		elapsed_min,elapsed_max,first_seen,last_seen,
		expected_empty_count,client_error_count,throttle_count,upstream_error_count
	)
	select from_unixtime(floor(unix_timestamp(l.created_at)/60)*60),l.interface_id,l.user_id,
	       max(coalesce(l.api_code,'')),count(*),sum(l.success=1),sum(` + statsFailureExpr + `),sum(l.billable=1),
	       sum(l.charge_amount),sum(l.point_price),sum(l.request_size),sum(l.response_size),
	       sum(l.elapsed_ms),min(l.elapsed_ms),max(l.elapsed_ms),min(l.created_at),max(l.created_at),
	       sum((` + callOutcomeClassSQL + `)=2),sum((` + callOutcomeClassSQL + `)=3),
	       sum((` + callOutcomeClassSQL + `)=4),sum((` + callOutcomeClassSQL + `)=5)
	from bp_interface_call_log l
	where ` + sourceEligibility + `
	group by from_unixtime(floor(unix_timestamp(l.created_at)/60)*60),l.interface_id,l.user_id
	on duplicate key update api_code=values(api_code),` + metricAdditions + `,` + outcomeMinuteAdditions

// apiDayUpsert is the low-cardinality projection consumed by the public
// homepage's 30-day ranking. It keeps the user dimension so current account
// status can still be applied at read time, while collapsing 1,440 minute
// buckets into one row per API/user/day.
var apiDayUpsert = `
	insert into bp_call_api_metric_day(
		bucket_date,interface_id,user_id,api_code,call_count,success_count,
		failure_count,elapsed_sum,first_seen,last_seen
	)
	select date(l.created_at),l.interface_id,l.user_id,max(coalesce(l.api_code,'')),
	       count(*),sum(l.success=1),sum(` + statsFailureExpr + `),sum(l.elapsed_ms),
	       min(l.created_at),max(l.created_at)
	from bp_interface_call_log l
	where ` + sourceEligibility + `
	group by date(l.created_at),l.interface_id,l.user_id
	on duplicate key update api_code=values(api_code),` + apiDayMetricAdditions

const apiDayMetricAdditions = `
	call_count=call_count+values(call_count),
	success_count=success_count+values(success_count),
	failure_count=failure_count+values(failure_count),
	elapsed_sum=elapsed_sum+values(elapsed_sum),
	first_seen=least(first_seen,values(first_seen)),
	last_seen=greatest(last_seen,values(last_seen))`

var userMinuteUpsert = `
	insert into bp_call_user_metric_minute(
		bucket_start,user_id,call_count,success_count,failure_count,billable_count,
		charge_amount,point_amount,request_bytes,response_bytes,elapsed_sum,
		elapsed_min,elapsed_max,first_seen,last_seen,
		expected_empty_count,client_error_count,throttle_count,upstream_error_count
	)
	select from_unixtime(floor(unix_timestamp(l.created_at)/60)*60),l.user_id,
	       count(*),sum(l.success=1),sum(` + statsFailureExpr + `),sum(l.billable=1),
	       sum(l.charge_amount),sum(l.point_price),sum(l.request_size),sum(l.response_size),
	       sum(l.elapsed_ms),min(l.elapsed_ms),max(l.elapsed_ms),min(l.created_at),max(l.created_at),
	       sum((` + callOutcomeClassSQL + `)=2),sum((` + callOutcomeClassSQL + `)=3),
	       sum((` + callOutcomeClassSQL + `)=4),sum((` + callOutcomeClassSQL + `)=5)
	from bp_interface_call_log l
	where ` + sourceEligibility + `
	group by from_unixtime(floor(unix_timestamp(l.created_at)/60)*60),l.user_id
	on duplicate key update ` + metricAdditions + `,` + outcomeMinuteAdditions

// regionIPHourUpsert 按 client_ip_hash（region 前缀的 keyed SHA-256）分组。
// 前缀在每次调用时解析：未配置密钥时与历史公开字面盐逐字节一致。
func regionIPHourUpsert() string {
	prefix := iphash.RegionSQLPrefix()
	return `
	insert into bp_call_region_ip_hour(
		bucket_start,user_id,country,province,city,isp,latitude,longitude,client_ip_hash,
		call_count,success_count,failure_count,request_bytes,response_bytes,elapsed_sum,first_seen,last_seen
	)
	select from_unixtime(floor(unix_timestamp(l.created_at)/3600)*3600),l.user_id,
	       coalesce(l.ip_country,''),coalesce(l.ip_province,''),coalesce(l.ip_city,''),coalesce(l.ip_isp,''),
	       coalesce(l.latitude,0),coalesce(l.longitude,0),
	       unhex(sha2(concat('` + prefix + `',coalesce(l.client_ip,'')),256)),
	       count(*),sum(l.success=1),sum(` + statsFailureExpr + `),sum(l.request_size),sum(l.response_size),
	       sum(l.elapsed_ms),min(l.created_at),max(l.created_at)
	from bp_interface_call_log l
	where ` + sourceEligibility + `
	group by from_unixtime(floor(unix_timestamp(l.created_at)/3600)*3600),l.user_id,
	         coalesce(l.ip_country,''),coalesce(l.ip_province,''),coalesce(l.ip_city,''),coalesce(l.ip_isp,''),
	         coalesce(l.latitude,0),coalesce(l.longitude,0),
	         unhex(sha2(concat('` + prefix + `',coalesce(l.client_ip,'')),256))
	on duplicate key update
	call_count=call_count+values(call_count),success_count=success_count+values(success_count),
	failure_count=failure_count+values(failure_count),request_bytes=request_bytes+values(request_bytes),
	response_bytes=response_bytes+values(response_bytes),elapsed_sum=elapsed_sum+values(elapsed_sum),
	first_seen=least(first_seen,values(first_seen)),last_seen=greatest(last_seen,values(last_seen))`
}

// regionHourUpsert intentionally omits the IP hash from its key. The distinct
// count is exact only inside this bounded source batch/hour/location/user
// contribution. Retries are safe because the checkpoint and every upsert run
// in one transaction; later batches add their count, yielding a bounded-cost
// estimate for dashboards rather than a cross-window exact distinct value.
var regionHourUpsert = `
	insert into bp_call_region_hour(
		bucket_start,user_id,country,province,city,isp,latitude,longitude,
		call_count,success_count,failure_count,unique_ip_count,
		request_bytes,response_bytes,elapsed_sum,first_seen,last_seen
	)
	select from_unixtime(floor(unix_timestamp(l.created_at)/3600)*3600),l.user_id,
	       coalesce(l.ip_country,''),coalesce(l.ip_province,''),coalesce(l.ip_city,''),coalesce(l.ip_isp,''),
	       coalesce(l.latitude,0),coalesce(l.longitude,0),
	       count(*),sum(l.success=1),sum(` + statsFailureExpr + `),
	       count(distinct nullif(trim(coalesce(l.client_ip,'')),'')),
	       sum(l.request_size),sum(l.response_size),sum(l.elapsed_ms),min(l.created_at),max(l.created_at)
	from bp_interface_call_log l
	where ` + sourceEligibility + `
	group by from_unixtime(floor(unix_timestamp(l.created_at)/3600)*3600),l.user_id,
	         coalesce(l.ip_country,''),coalesce(l.ip_province,''),coalesce(l.ip_city,''),coalesce(l.ip_isp,''),
	         coalesce(l.latitude,0),coalesce(l.longitude,0)
	on duplicate key update
	call_count=call_count+values(call_count),success_count=success_count+values(success_count),
	failure_count=failure_count+values(failure_count),unique_ip_count=unique_ip_count+values(unique_ip_count),
	request_bytes=request_bytes+values(request_bytes),response_bytes=response_bytes+values(response_bytes),
	elapsed_sum=elapsed_sum+values(elapsed_sum),first_seen=least(first_seen,values(first_seen)),
	last_seen=greatest(last_seen,values(last_seen))`

var failureIPHourUpsert = `
	insert into bp_call_failure_ip_hour(
		bucket_start,client_ip,user_id,interface_id,api_code,country,province,city,
		failure_count,elapsed_sum,first_seen,last_seen
	)
	select from_unixtime(floor(unix_timestamp(l.created_at)/3600)*3600),coalesce(l.client_ip,''),
	       l.user_id,l.interface_id,max(coalesce(l.api_code,'')),max(coalesce(l.ip_country,'')),
	       max(coalesce(l.ip_province,'')),max(coalesce(l.ip_city,'')),count(*),sum(l.elapsed_ms),
	       min(l.created_at),max(l.created_at)
	from bp_interface_call_log l
	where ` + sourceEligibility + ` and ` + statsFailureExpr + ` and coalesce(l.client_ip,'')<>''
	group by from_unixtime(floor(unix_timestamp(l.created_at)/3600)*3600),l.client_ip,l.user_id,l.interface_id
	on duplicate key update api_code=values(api_code),country=values(country),province=values(province),city=values(city),
	failure_count=failure_count+values(failure_count),elapsed_sum=elapsed_sum+values(elapsed_sum),
	first_seen=least(first_seen,values(first_seen)),last_seen=greatest(last_seen,values(last_seen))`

func failureUserHourUpsert() string {
	prefix := iphash.FailureSQLPrefix()
	return `
	insert into bp_call_failure_user_hour(
		bucket_start,user_id,interface_id,api_code,client_ip_hash,has_client_ip,
		failure_count,elapsed_sum,first_seen,last_seen
	)
	select from_unixtime(floor(unix_timestamp(l.created_at)/3600)*3600),l.user_id,l.interface_id,
	       max(coalesce(l.api_code,'')),unhex(sha2(concat('` + prefix + `',coalesce(l.client_ip,'')),256)),
	       max(coalesce(l.client_ip,'')<>''),count(*),sum(l.elapsed_ms),min(l.created_at),max(l.created_at)
	from bp_interface_call_log l
	where ` + sourceEligibility + ` and ` + statsFailureExpr + `
	group by from_unixtime(floor(unix_timestamp(l.created_at)/3600)*3600),l.user_id,l.interface_id,
	         unhex(sha2(concat('` + prefix + `',coalesce(l.client_ip,'')),256))
	on duplicate key update api_code=values(api_code),has_client_ip=greatest(has_client_ip,values(has_client_ip)),
	failure_count=failure_count+values(failure_count),elapsed_sum=elapsed_sum+values(elapsed_sum),
	first_seen=least(first_seen,values(first_seen)),last_seen=greatest(last_seen,values(last_seen))`
}

func riskFingerprintUpsert() string {
	prefix := iphash.RiskSQLPrefix()
	return `
	insert into bp_call_risk_fingerprint_5m(
		bucket_start,interface_id,request_fingerprint,user_id,api_key_id,client_ip_hash,
		has_client_ip,client_ip,api_code,country,province,city,call_count,success_count,
		failure_count,elapsed_sum,first_seen,last_seen
	)
	select from_unixtime(floor(unix_timestamp(l.created_at)/300)*300),l.interface_id,l.request_fingerprint,
	       l.user_id,coalesce(l.api_key_id,0),
	       unhex(sha2(concat('` + prefix + `',coalesce(l.client_ip,'')),256)),
	       max(coalesce(l.client_ip,'')<>''),max(coalesce(l.client_ip,'')),max(coalesce(l.api_code,'')),
	       max(coalesce(l.ip_country,'')),max(coalesce(l.ip_province,'')),max(coalesce(l.ip_city,'')),
	       count(*),sum(l.success=1),sum(l.success=0),sum(l.elapsed_ms),min(l.created_at),max(l.created_at)
	from bp_interface_call_log l
	where ` + sourceEligibility + ` and coalesce(l.request_fingerprint,'')<>''
	group by from_unixtime(floor(unix_timestamp(l.created_at)/300)*300),l.interface_id,l.request_fingerprint,
	         l.user_id,coalesce(l.api_key_id,0),
	         unhex(sha2(concat('` + prefix + `',coalesce(l.client_ip,'')),256))
	on duplicate key update has_client_ip=greatest(has_client_ip,values(has_client_ip)),client_ip=values(client_ip),
	api_code=values(api_code),country=values(country),province=values(province),city=values(city),
	call_count=call_count+values(call_count),success_count=success_count+values(success_count),
	failure_count=failure_count+values(failure_count),elapsed_sum=elapsed_sum+values(elapsed_sum),
	first_seen=least(first_seen,values(first_seen)),last_seen=greatest(last_seen,values(last_seen))`
}
