package analytics

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// This file owns the read-only diagnostics for the rollup pipeline.
//
// The two authoritative readers — the admin dashboard (internal/domain/user)
// and the homepage (internal/domain/content) — can only answer "ready" or
// "degraded". Neither says *why*, so diagnosing "部分指标待更新" used to require
// a MySQL client and a log grep on the production host.
//
// Diagnostics() observes exactly the durable state those readers consult. Every
// read is O(1) and strictly read-only: it never advances a cursor, never
// schedules a repair and never certifies a number. Its verdict is advisory —
// the truth envelope in the HTTP responses stays the only authority.

const (
	diagnosticRepairPhaseClear    = "CLEAR"
	diagnosticRepairPhaseRollup   = "ROLLUP"
	diagnosticRepairPhaseFinalize = "FINALIZE"

	diagnosticRepairStatusPending = "PENDING"
	diagnosticRepairStatusRunning = "RUNNING"
)

// Stall windows mirror the canonical readers. They are restated instead of
// imported because internal/domain/content deliberately does not import this
// package (see content/analytics_health.go) and the user domain keeps its own
// unexported copy. A test pins these literals so drift in either canonical
// reader fails here instead of silently disagreeing with the UI.
const (
	// DiagnosticRepairStallAfter mirrors user.adminAnalyticsRepairStallAfter.
	DiagnosticRepairStallAfter = 30 * time.Minute
	// DiagnosticWorkerStallAfter mirrors content.homeAnalyticsWorkerStallAfter.
	DiagnosticWorkerStallAfter = 5 * time.Minute
)

// Diagnostics is a read-only observation of the durable rollup pipeline.
type Diagnostics struct {
	// CheckpointPresent is false when the checkpoint row does not exist yet,
	// which means the worker has never committed a batch on this dataset.
	CheckpointPresent bool

	LastLogID        int64
	LastLogCreatedAt sql.NullTime
	LastBatchSize    int
	LastStartedAt    sql.NullTime
	LastCompletedAt  sql.NullTime
	LastError        string

	// RepairColumnsPresent is false on a schema older than migration 000090,
	// where the durable bounded repair task does not exist yet.
	RepairColumnsPresent bool
	RepairStatus         string
	RepairPhase          string
	RepairProjectionMask uint64
	RepairRequestedAt    sql.NullTime
	RepairStartedAt      sql.NullTime
	RepairCompletedAt    sql.NullTime
	RepairCursorID       int64
	RepairUpperLogID     int64

	NewestLogID        int64
	NewestLogCreatedAt sql.NullTime
}

// DiagnosticCause is the suspected cause of a degraded dashboard.
type DiagnosticCause string

const (
	CauseHealthy           DiagnosticCause = "healthy"
	CauseWorkerDisabled    DiagnosticCause = "worker_disabled"
	CauseCheckpointMissing DiagnosticCause = "checkpoint_missing"
	CauseWorkerNeverRan    DiagnosticCause = "worker_never_ran"
	CauseRepairRebuilding  DiagnosticCause = "repair_rebuilding"
	CauseRepairStalled     DiagnosticCause = "repair_stalled"
	CauseRepairStateUnaged DiagnosticCause = "repair_state_unaged"
	CauseWorkerStalled     DiagnosticCause = "worker_stalled"
	CauseRollupFailing     DiagnosticCause = "rollup_failing"
	CauseRollupBacklog     DiagnosticCause = "rollup_backlog"
)

// DiagnosticInput carries the observations that do not come from the database.
type DiagnosticInput struct {
	// WorkerEnabled mirrors analytics.enabled. A disabled worker never fills
	// the rollup tables, so every traffic metric stays unavailable.
	WorkerEnabled bool
	// Now is the reference clock. Zero disables all age-based judgements.
	Now time.Time
}

// RepairActive reports whether a durable bounded repair is still in flight.
// It mirrors user.adminAnalyticsRepairState.active, including the phase-based
// fallback used when a schema only carries the phase column.
func (d Diagnostics) RepairActive() bool {
	switch d.RepairStatus {
	case diagnosticRepairStatusPending, diagnosticRepairStatusRunning:
		return true
	}
	switch d.RepairPhase {
	case diagnosticRepairPhaseClear, diagnosticRepairPhaseRollup, diagnosticRepairPhaseFinalize:
		return true
	}
	return false
}

// Behind reports whether the rollup watermark trails the newest source row.
// A dataset with no source rows at all is not behind: zero is the answer.
func (d Diagnostics) Behind() bool {
	return d.NewestLogID > 0 && d.LastLogID < d.NewestLogID
}

// LagSeconds is how far the published numbers trail the newest source row.
func (d Diagnostics) LagSeconds() int64 {
	if !d.NewestLogCreatedAt.Valid || !d.LastLogCreatedAt.Valid {
		return 0
	}
	if !d.NewestLogCreatedAt.Time.After(d.LastLogCreatedAt.Time) {
		return 0
	}
	return int64(d.NewestLogCreatedAt.Time.Sub(d.LastLogCreatedAt.Time) / time.Second)
}

// completionStalled reports whether the worker has stopped committing batches.
//
// It ages the *completion* clock, mirroring the homepage reader. The start clock
// is deliberately not used: ProcessBatch stamps last_started_at (and clears
// last_error) at the top of every batch, so a worker that keeps retrying and
// keeps failing still looks busy by that measure. Only a committed batch is
// evidence of progress.
func (d Diagnostics) completionStalled(now time.Time) bool {
	if !d.LastCompletedAt.Valid {
		// A checkpoint that never recorded a completion has published nothing.
		// Whether the worker is running but failing, or not running at all, is
		// decided by the caller from last_started_at and last_error.
		return true
	}
	return !now.IsZero() && now.Sub(d.LastCompletedAt.Time) > DiagnosticWorkerStallAfter
}

// repairStalled mirrors user.adminAnalyticsRepairState, which ages a bounded
// repair by its persisted request time rather than by batch activity.
func (d Diagnostics) repairStalled(now time.Time) bool {
	return !now.IsZero() && d.RepairRequestedAt.Valid &&
		now.Sub(d.RepairRequestedAt.Time) > DiagnosticRepairStallAfter
}

// Diagnose maps the observation set onto the suspected cause, in the order an
// operator should act: can it possibly work, did it ever start, is it stuck,
// then is it merely behind.
func (d Diagnostics) Diagnose(in DiagnosticInput) DiagnosticCause {
	if !in.WorkerEnabled {
		return CauseWorkerDisabled
	}
	if !d.CheckpointPresent {
		return CauseCheckpointMissing
	}
	if d.RepairActive() {
		// An un-ageable repair cannot ever be released: without requested_at
		// the readers keep the conservative suppression forever.
		if !d.RepairRequestedAt.Valid {
			return CauseRepairStateUnaged
		}
		// "Never started" is categorically different from "started and stopped":
		// the first means the worker is not running at all, the second means it
		// is running but failing.
		if !d.RepairStartedAt.Valid {
			return CauseWorkerNeverRan
		}
		if d.repairStalled(in.Now) {
			return CauseRepairStalled
		}
		// An unfinished repair holds the projection-backed sections back even
		// when the watermark has already caught up: the readers suppress what
		// the rebuild is still rewriting.
		return CauseRepairRebuilding
	}
	if !d.Behind() {
		return CauseHealthy
	}
	if !d.completionStalled(in.Now) {
		// Behind, but the worker is committing: ordinary catch-up, not a fault.
		return CauseRollupBacklog
	}
	if !d.LastCompletedAt.Valid && !d.LastStartedAt.Valid {
		return CauseWorkerNeverRan
	}
	if strings.TrimSpace(d.LastError) != "" {
		// Retrying and failing. This is the shape that used to be invisible: the
		// inbox of the dashboard shows dashes and nothing else says why.
		return CauseRollupFailing
	}
	return CauseWorkerStalled
}

// Diagnostics observes the checkpoint, the durable repair task and the newest
// source row. It tolerates every historical schema shape: the repair columns
// appeared in migrations 000090 and 000092, so a deployment that runs against
// an older database degrades this report instead of failing it.
func (r *Repository) Diagnostics(ctx context.Context) (Diagnostics, error) {
	var item Diagnostics
	if r == nil || r.db == nil {
		return item, errors.New("analytics repository database is nil")
	}
	item, err := r.diagnosticsWithRepairColumns(ctx)
	switch {
	case err == nil:
		return r.attachNewestLog(ctx, item)
	case errors.Is(err, sql.ErrNoRows):
		return item, nil
	case isMissingRepairCursorColumnError(err):
		item, err = r.diagnosticsWithBaseRepairColumns(ctx)
	case isMissingRepairColumnError(err):
		item, err = r.diagnosticsWithoutRepairColumns(ctx)
	default:
		return item, err
	}
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return item, nil
		}
		return Diagnostics{}, err
	}
	return r.attachNewestLog(ctx, item)
}

func (r *Repository) diagnosticsWithRepairColumns(ctx context.Context) (Diagnostics, error) {
	var item Diagnostics
	err := r.db.QueryRowContext(ctx, `
		select last_log_id,last_log_created_at,last_batch_size,last_started_at,last_completed_at,
		       coalesce(last_error,''),
		       coalesce(repair_status,''),coalesce(repair_phase,''),coalesce(repair_projection_mask,0),
		       repair_requested_at,repair_started_at,repair_completed_at,
		       coalesce(repair_cursor_id,0),coalesce(repair_upper_log_id,0)
		from bp_analytics_checkpoint where worker_key=?
	`, CheckpointKey).Scan(
		&item.LastLogID, &item.LastLogCreatedAt, &item.LastBatchSize,
		&item.LastStartedAt, &item.LastCompletedAt, &item.LastError,
		&item.RepairStatus, &item.RepairPhase, &item.RepairProjectionMask,
		&item.RepairRequestedAt, &item.RepairStartedAt, &item.RepairCompletedAt,
		&item.RepairCursorID, &item.RepairUpperLogID,
	)
	if err != nil {
		return item, err
	}
	item.CheckpointPresent = true
	item.RepairColumnsPresent = true
	return item, nil
}

// diagnosticsWithBaseRepairColumns covers migration 000090, which persisted the
// durable task without the resumable cursor, projection mask or phase columns.
func (r *Repository) diagnosticsWithBaseRepairColumns(ctx context.Context) (Diagnostics, error) {
	var item Diagnostics
	err := r.db.QueryRowContext(ctx, `
		select last_log_id,last_log_created_at,last_batch_size,last_started_at,last_completed_at,
		       coalesce(last_error,''),
		       coalesce(repair_status,''),repair_requested_at,repair_started_at,repair_completed_at
		from bp_analytics_checkpoint where worker_key=?
	`, CheckpointKey).Scan(
		&item.LastLogID, &item.LastLogCreatedAt, &item.LastBatchSize,
		&item.LastStartedAt, &item.LastCompletedAt, &item.LastError,
		&item.RepairStatus, &item.RepairRequestedAt, &item.RepairStartedAt, &item.RepairCompletedAt,
	)
	if err != nil {
		return item, err
	}
	item.CheckpointPresent = true
	item.RepairColumnsPresent = true
	return item, nil
}

// diagnosticsWithoutRepairColumns covers every schema older than 000090.
func (r *Repository) diagnosticsWithoutRepairColumns(ctx context.Context) (Diagnostics, error) {
	var item Diagnostics
	err := r.db.QueryRowContext(ctx, `
		select last_log_id,last_log_created_at,last_batch_size,last_started_at,last_completed_at,
		       coalesce(last_error,'')
		from bp_analytics_checkpoint where worker_key=?
	`, CheckpointKey).Scan(
		&item.LastLogID, &item.LastLogCreatedAt, &item.LastBatchSize,
		&item.LastStartedAt, &item.LastCompletedAt, &item.LastError,
	)
	if err != nil {
		return item, err
	}
	item.CheckpointPresent = true
	return item, nil
}

// attachNewestLog probes the newest source row through the primary key, which
// InnoDB resolves with a single leaf read. The probe is best effort: a failure
// must not hide the checkpoint facts that were already read.
func (r *Repository) attachNewestLog(ctx context.Context, item Diagnostics) (Diagnostics, error) {
	var (
		id        int64
		createdAt sql.NullTime
	)
	err := r.db.QueryRowContext(ctx, `
		select id,created_at from bp_interface_call_log order by id desc limit 1
	`).Scan(&id, &createdAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return item, nil
		}
		return item, err
	}
	item.NewestLogID = id
	item.NewestLogCreatedAt = createdAt
	return item, nil
}
