package content

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// This file owns the homepage's analytics truth contract.
//
// The public homepage answers from a persisted rollup snapshot, never from a
// live scan of the append-only call log. That makes the request path cheap, but
// it also means the numbers can be *unavailable* (rollup worker disabled,
// snapshot never primed, aggregation stalled) without being *zero*. Presenting
// the two identically is the failure mode this contract exists to prevent:
// a visitor must never read a real "0 calls in 24h" when the truth is "we do not
// know yet".
//
// Every reader therefore receives an AnalyticsHealth verdict alongside the
// numbers and is required to render the unavailable state instead of the zero.

const (
	// homeAnalyticsCheckpointKey mirrors analytics.CheckpointKey. The content
	// domain deliberately does not import the analytics domain, so the contract
	// is restated here and pinned by a contract test.
	homeAnalyticsCheckpointKey = "call_log_rollup"

	// homeAnalyticsWorkerStallAfter bounds how long the rollup worker may make
	// no progress while unprocessed call logs already exist. Polling runs every
	// second and drains up to twenty batches per tick, so five minutes of zero
	// progress with a non-empty backlog means the worker is gone, not busy.
	homeAnalyticsWorkerStallAfter = 5 * time.Minute

	// homeAnalyticsSnapshotStallAfter bounds the age of the persisted snapshot.
	// The background refresh runs every minute, so a snapshot older than this
	// means the refresh chain itself is broken even if the rollup is healthy.
	homeAnalyticsSnapshotStallAfter = 10 * time.Minute

	// homeAnalyticsCoveredUpToLayout matches the datetime shape already used by
	// other content responses (site/notice UpdateTime) so clients parse one
	// format everywhere.
	homeAnalyticsCoveredUpToLayout = "2006-01-02 15:04:05"
)

// Degraded section names reported through PlatformStats.DegradedSections.
const (
	AnalyticsSectionRollup   = "rollup"
	AnalyticsSectionSnapshot = "snapshot"
	AnalyticsSectionDisabled = "analytics"
)

// homeAnalyticsUnavailableFields lists every PlatformStats field whose value is
// derived from call-traffic rollups. apiTotal, enabledApiTotal and userTotal are
// intentionally absent: they are counted from bp_interface_api and bp_user, so
// they stay trustworthy while aggregation is degraded.
var homeAnalyticsUnavailableFields = []string{
	"activeRegions24h",
	"activeRegions24hDelta",
	"availabilityPercent",
	"calls24h",
	"calls24hDeltaPercent",
	"peakQps24h",
	"peakQps24hDelta",
}

// AnalyticsHealth is the homepage's verdict on whether the analytics numbers it
// is about to publish can be certified as real.
type AnalyticsHealth struct {
	// Enabled mirrors analytics.enabled. A disabled worker never fills the
	// rollup tables, so every traffic metric is unavailable rather than zero.
	Enabled bool
	// Ready is true only when every degraded condition is absent.
	Ready bool
	// DegradedSections names the layers that could not certify their numbers.
	DegradedSections []string
	// UnavailableFields names the PlatformStats JSON fields that must not be
	// rendered as values. It is empty whenever Ready is true.
	UnavailableFields []string
	// CoveredUpTo is the newest call-log timestamp the published numbers
	// actually include. Zero means no coverage at all.
	CoveredUpTo time.Time
	// LagSeconds is how far the published numbers trail the newest call log.
	// It is zero when the rollups are caught up, and it stays small on an idle
	// site because it compares against the newest log, not against now.
	LagSeconds int64
}

// CoveredUpToText renders the coverage watermark in the shared datetime shape.
func (h AnalyticsHealth) CoveredUpToText() string {
	return formatCoveredUpTo(h.CoveredUpTo)
}

// formatCoveredUpTo renders a coverage watermark, or an empty string when there
// is no coverage to report.
func formatCoveredUpTo(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.Format(homeAnalyticsCoveredUpToLayout)
}

// parseCoveredUpTo reads a watermark previously rendered by formatCoveredUpTo.
// Callers treat an unparseable value as "not recorded" rather than as an error.
func parseCoveredUpTo(raw string) time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}
	}
	parsed, err := time.ParseInLocation(homeAnalyticsCoveredUpToLayout, raw, time.Local)
	if err != nil {
		return time.Time{}
	}
	return parsed
}

// applyAnalyticsHealth stamps the truth envelope onto a set of stats. Both
// slices are normalized to non-nil so healthy responses still serialize
// `"degradedSections":[]` instead of `null`.
func applyAnalyticsHealth(stats PlatformStats, health AnalyticsHealth) PlatformStats {
	stats.AnalyticsReady = health.Ready
	stats.DegradedSections = append([]string{}, health.DegradedSections...)
	stats.UnavailableFields = append([]string{}, health.UnavailableFields...)
	stats.CoveredUpTo = health.CoveredUpToText()
	stats.LagSeconds = health.LagSeconds
	return stats
}

// stripAnalyticsHealth clears the read-time verdict before a payload is
// persisted. Storing it would let a stale "ready" claim outlive the condition
// that justified it.
func stripAnalyticsHealth(stats PlatformStats) PlatformStats {
	stats.AnalyticsReady = false
	stats.DegradedSections = nil
	stats.UnavailableFields = nil
	stats.CoveredUpTo = ""
	stats.LagSeconds = 0
	return stats
}

// unavailableAnalyticsHealth is the fail-closed verdict used when the health
// probe itself fails. Publishing numbers without a verdict would be exactly the
// "silent zero" this contract forbids, so the reader is told they are
// unavailable.
func unavailableAnalyticsHealth(workerEnabled bool) AnalyticsHealth {
	return AnalyticsHealth{
		Enabled:           workerEnabled,
		Ready:             false,
		DegradedSections:  []string{AnalyticsSectionRollup},
		UnavailableFields: append([]string(nil), homeAnalyticsUnavailableFields...),
	}
}

// analyticsHealthInput is the raw observation set. Keeping it a plain struct
// makes the decision table testable without a database.
type analyticsHealthInput struct {
	RollupLastLogID        int64
	RollupLastLogCreatedAt time.Time
	RollupLastCompletedAt  time.Time

	NewestLogID        int64
	NewestLogCreatedAt time.Time

	SnapshotSourceUpdatedAt time.Time
	SnapshotCoveredUpTo     time.Time

	WorkerEnabled bool
	Now           time.Time
}

// evaluateAnalyticsHealth applies the truth decision table.
//
// Ordered from "cannot possibly know" to "knows but is behind":
//
//  1. worker disabled            -> analytics
//  2. snapshot never primed      -> snapshot
//  3. snapshot refresh stalled   -> snapshot
//  4. traffic exists but the
//     rollup never produced one  -> rollup
//  5. rollup stalled on backlog  -> rollup
//
// An idle site with no call logs at all is Ready: zero really is the answer.
func evaluateAnalyticsHealth(in analyticsHealthInput) AnalyticsHealth {
	health := AnalyticsHealth{Enabled: in.WorkerEnabled}
	degraded := make([]string, 0, 3)
	add := func(section string) {
		for _, existing := range degraded {
			if existing == section {
				return
			}
		}
		degraded = append(degraded, section)
	}

	if !in.WorkerEnabled {
		add(AnalyticsSectionDisabled)
	}
	if in.SnapshotSourceUpdatedAt.IsZero() {
		add(AnalyticsSectionSnapshot)
	} else if !in.Now.IsZero() && in.Now.Sub(in.SnapshotSourceUpdatedAt) > homeAnalyticsSnapshotStallAfter {
		add(AnalyticsSectionSnapshot)
	}
	// Nothing to summarize when no call log has ever been recorded: zero is the
	// honest answer and the site is not degraded.
	if in.NewestLogID > 0 {
		// Both a missing watermark and a watermark behind the newest log are
		// only defects while the pipeline is idle. A running repair clears the
		// watermark on purpose, and a burst legitimately leaves a backlog, so
		// degrading on either alone would flap on healthy deployments.
		behind := in.RollupLastLogCreatedAt.IsZero() || in.RollupLastLogID < in.NewestLogID
		if behind && rollupPipelineIdle(in) {
			add(AnalyticsSectionRollup)
		}
	}

	health.DegradedSections = degraded
	health.Ready = len(degraded) == 0
	health.CoveredUpTo = coverageWatermark(in)
	if in.NewestLogCreatedAt.After(health.CoveredUpTo) {
		health.LagSeconds = int64(in.NewestLogCreatedAt.Sub(health.CoveredUpTo) / time.Second)
	}
	if !health.Ready {
		health.UnavailableFields = append([]string(nil), homeAnalyticsUnavailableFields...)
	}
	return health
}

// rollupPipelineIdle reports that the rollup worker has stopped making
// progress, either because it never completed a batch or because its last
// completion is older than the stall window.
func rollupPipelineIdle(in analyticsHealthInput) bool {
	if in.RollupLastCompletedAt.IsZero() {
		return true
	}
	return !in.Now.IsZero() && in.Now.Sub(in.RollupLastCompletedAt) > homeAnalyticsWorkerStallAfter
}

// coverageWatermark prefers the snapshot's own recorded watermark. It falls back
// to the snapshot write time so an older payload that predates the field still
// reports something meaningful instead of an empty string.
func coverageWatermark(in analyticsHealthInput) time.Time {
	if !in.SnapshotCoveredUpTo.IsZero() {
		return in.SnapshotCoveredUpTo
	}
	if !in.SnapshotSourceUpdatedAt.IsZero() {
		return in.SnapshotSourceUpdatedAt
	}
	return in.RollupLastLogCreatedAt
}

// LoadAnalyticsHealth observes the rollup checkpoint, the newest call log and
// the persisted snapshot metadata, then applies the decision table.
//
// Every read is O(1): two primary-key lookups, one rightmost-index probe and a
// single-row read. Nothing here scans the append-only log.
//
// A missing analytics schema degrades the verdict instead of failing the
// homepage, so an application running against a pre-rollup database reports
// "unavailable" rather than a confident zero.
func (r *Repository) LoadAnalyticsHealth(
	ctx context.Context,
	snapshotSourceUpdatedAt time.Time,
	snapshotCoveredUpTo time.Time,
	workerEnabled bool,
	now time.Time,
) (AnalyticsHealth, error) {
	in := analyticsHealthInput{
		SnapshotSourceUpdatedAt: snapshotSourceUpdatedAt,
		SnapshotCoveredUpTo:     snapshotCoveredUpTo,
		WorkerEnabled:           workerEnabled,
		Now:                     now,
	}
	if r == nil || r.db == nil {
		return evaluateAnalyticsHealth(in), nil
	}
	newestID, newestAt, err := r.newestCallLog(ctx)
	if err != nil {
		return AnalyticsHealth{}, err
	}
	in.NewestLogID, in.NewestLogCreatedAt = newestID, newestAt

	checkpoint, err := r.loadRollupCheckpoint(ctx)
	if err != nil {
		return AnalyticsHealth{}, err
	}
	in.RollupLastLogID = checkpoint.lastLogID
	in.RollupLastLogCreatedAt = checkpoint.lastLogCreatedAt
	in.RollupLastCompletedAt = checkpoint.lastCompletedAt

	return evaluateAnalyticsHealth(in), nil
}

type rollupCheckpointLite struct {
	lastLogID        int64
	lastLogCreatedAt time.Time
	lastCompletedAt  time.Time
}

// loadRollupCheckpoint tolerates both a missing checkpoint row and a pre-rollup
// schema; either way the worker has produced no coverage yet.
func (r *Repository) loadRollupCheckpoint(ctx context.Context) (rollupCheckpointLite, error) {
	var (
		item        rollupCheckpointLite
		createdAt   sql.NullTime
		completedAt sql.NullTime
	)
	err := r.db.QueryRowContext(ctx, `
		select last_log_id,last_log_created_at,last_completed_at
		from bp_analytics_checkpoint where worker_key=?
	`, homeAnalyticsCheckpointKey).Scan(&item.lastLogID, &createdAt, &completedAt)
	if err == nil {
		item.lastLogCreatedAt = nullableTime(createdAt)
		item.lastCompletedAt = nullableTime(completedAt)
		return item, nil
	}
	if errors.Is(err, sql.ErrNoRows) || isMissingAnalyticsSchemaError(err) {
		return rollupCheckpointLite{}, nil
	}
	return rollupCheckpointLite{}, err
}

// newestCallLog reads the newest append-only row through the primary key. InnoDB
// resolves a descending primary-key range with a single leaf read, so this stays
// constant-time no matter how many rows the log holds.
func (r *Repository) newestCallLog(ctx context.Context) (int64, time.Time, error) {
	var (
		id        int64
		createdAt time.Time
	)
	err := r.db.QueryRowContext(ctx, `
		select id,created_at from bp_interface_call_log order by id desc limit 1
	`).Scan(&id, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, time.Time{}, nil
	}
	if err != nil {
		return 0, time.Time{}, err
	}
	return id, createdAt, nil
}

func nullableTime(value sql.NullTime) time.Time {
	if !value.Valid {
		return time.Time{}
	}
	return value.Time
}

// isMissingAnalyticsSchemaError reports the MySQL codes raised when the rollup
// schema has not been created yet (unknown table/column) or when an older
// binary runs against a newer, differently shaped checkpoint table.
func isMissingAnalyticsSchemaError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "error 1176") || strings.Contains(message, " 1176 ") {
		return false
	}
	return strings.Contains(message, "error 1146") ||
		strings.Contains(message, "error 1054") ||
		strings.Contains(message, "42s02") ||
		strings.Contains(message, "42s22") ||
		strings.Contains(message, "no such table") ||
		strings.Contains(message, "unknown column") ||
		strings.Contains(message, "doesn't exist")
}
