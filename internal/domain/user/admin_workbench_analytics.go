package user

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

const adminAnalyticsCheckpointKey = "call_log_rollup"

// adminAnalyticsRepairStallAfter bounds how long a bounded projection repair may
// keep the dashboard's analytics sections suppressed.
//
// While a repair is active those sections are skipped on purpose so an HTTP
// request never races the rebuild. That protection is only correct while the
// repair actually advances: the serial analytics worker owns it, so a deployment
// with analytics disabled (analytics.enabled=false / ANALYTICS_ENABLED=false),
// a crashed worker, or a repair that keeps failing would otherwise leave every
// analytics section at zero forever — indistinguishable from real business data.
//
// After this window the request stops suppressing the sections and reads
// whatever prior rollups or committed repair chunks currently hold. The worker
// serializes repair before incremental ingestion, so those values may be
// incomplete and are diagnostic only. The stalled state is reported through
// analyticsReady/degradedSections and an explicit WARN log. Releasing read
// suppression never certifies a still-active repair as complete.
const adminAnalyticsRepairStallAfter = 30 * time.Minute

// These bits are persisted by migrations 000090/000092. Keep the ordering in
// sync with internal/domain/analytics/repair.go and the migration contract.
const (
	adminProjectionMetricMinute uint16 = 1 << iota
	adminProjectionAPIMinute
	adminProjectionAPIDay
	adminProjectionUserMinute
	adminProjectionRegionIPHour
	adminProjectionRegionHour
	adminProjectionFailureIPHour
	adminProjectionFailureUserHour
	adminProjectionRiskFingerprint
	adminProjectionMetricDay
	adminProjectionUserMetricDay
)

const adminAnalyticsProjectionMask = adminProjectionMetricMinute |
	adminProjectionAPIMinute |
	adminProjectionAPIDay |
	adminProjectionUserMinute |
	adminProjectionRegionIPHour |
	adminProjectionRegionHour |
	adminProjectionFailureIPHour |
	adminProjectionFailureUserHour |
	adminProjectionRiskFingerprint |
	adminProjectionMetricDay |
	adminProjectionUserMetricDay

var errAdminAnalyticsProjectionUnavailable = errors.New("analytics projection is rebuilding")

type adminAnalyticsRepairState struct {
	active         bool
	projectionMask uint16
	// phase/status are carried for diagnostics only; degrades() never reads them.
	phase  string
	status string
	// requestedAt is the persisted repair request time. A zero value means the
	// schema/row predates the column or the repair was never requested.
	requestedAt time.Time
	// lastError is the analytics worker's most recent failure text.
	lastError string
	// stalled reports that the repair stayed active past
	// adminAnalyticsRepairStallAfter, so suppression must be released.
	stalled bool
}

// suppresses reports whether the repair is still inside its protected window.
func (s adminAnalyticsRepairState) suppresses() bool {
	return s.active && !s.stalled
}

func (s adminAnalyticsRepairState) degrades(section AdminWorkbenchSection) bool {
	if !s.active {
		return false
	}
	mask := s.projectionMask
	if mask == 0 {
		// Migration 000090 did not persist a per-projection mask. Its repair
		// rebuilds every projection, so conservatively protect every analytics
		// section while an older binary/schema combination is being upgraded.
		mask = adminAnalyticsProjectionMask
	}
	switch section {
	case AdminWorkbenchSectionOverview:
		return mask&adminProjectionMetricMinute != 0
	case AdminWorkbenchSectionCallTrend:
		return mask&(adminProjectionMetricMinute|adminProjectionMetricDay) != 0
	case AdminWorkbenchSectionHotAPIs:
		return mask&(adminProjectionAPIMinute|adminProjectionAPIDay) != 0
	case AdminWorkbenchSectionActiveUsers:
		return mask&(adminProjectionUserMinute|adminProjectionUserMetricDay) != 0
	case AdminWorkbenchSectionAccess:
		return mask&adminProjectionRegionHour != 0
	default:
		return false
	}
}

func (r *Repository) adminAnalyticsRepairState(ctx context.Context, now time.Time) (adminAnalyticsRepairState, error) {
	var status, phase, lastError string
	var projectionMask uint64
	var requestedAt sql.NullTime
	err := r.db.QueryRowContext(ctx, `
		select coalesce(repair_status,'NONE'),
		       coalesce(repair_projection_mask,0),
		       coalesce(repair_phase,'IDLE'),
		       repair_requested_at,
		       coalesce(last_error,'')
		from bp_analytics_checkpoint
		where worker_key=?
		limit 1
	`, adminAnalyticsCheckpointKey).Scan(&status, &projectionMask, &phase, &requestedAt, &lastError)
	if err == nil {
		return newAdminAnalyticsRepairState(status, phase, projectionMask, requestedAt, lastError, now), nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return adminAnalyticsRepairState{}, nil
	}
	if !isMissingAdminAnalyticsCursorColumnError(err) {
		return adminAnalyticsRepairState{}, err
	}

	// Schema 090 has only repair_status. Preserve rolling-upgrade support while
	// still preventing HTTP requests from racing its all-projection rebuild.
	err = r.db.QueryRowContext(ctx, `
		select coalesce(repair_status,'NONE'),
		       repair_requested_at,
		       coalesce(last_error,'')
		from bp_analytics_checkpoint
		where worker_key=?
		limit 1
	`, adminAnalyticsCheckpointKey).Scan(&status, &requestedAt, &lastError)
	if err == nil {
		return newAdminAnalyticsRepairState(status, "IDLE", 0, requestedAt, lastError, now), nil
	}
	if errors.Is(err, sql.ErrNoRows) || isMissingTableError(err) || isMissingAdminAnalyticsRepairColumnError(err) {
		return adminAnalyticsRepairState{}, nil
	}
	return adminAnalyticsRepairState{}, err
}

func newAdminAnalyticsRepairState(status, phase string, projectionMask uint64, requestedAt sql.NullTime, lastError string, now time.Time) adminAnalyticsRepairState {
	normalizedStatus := strings.ToUpper(strings.TrimSpace(status))
	normalizedPhase := strings.ToUpper(strings.TrimSpace(phase))
	active := normalizedStatus == "PENDING" || normalizedStatus == "RUNNING"
	if normalizedPhase == "CLEAR" || normalizedPhase == "ROLLUP" || normalizedPhase == "FINALIZE" {
		active = true
	}
	state := adminAnalyticsRepairState{
		active:         active,
		projectionMask: uint16(projectionMask & uint64(adminAnalyticsProjectionMask)),
		phase:          normalizedPhase,
		status:         normalizedStatus,
		lastError:      strings.TrimSpace(lastError),
	}
	if requestedAt.Valid {
		state.requestedAt = requestedAt.Time
	}
	// A repair with no recorded request time cannot be aged out; keep the
	// conservative suppression for it. An unset clock (zero now) is only
	// produced by callers that do not track time and must not age anything out.
	if active && !state.requestedAt.IsZero() && !now.IsZero() &&
		now.Sub(state.requestedAt) > adminAnalyticsRepairStallAfter {
		state.stalled = true
	}
	return state
}

func isMissingAdminAnalyticsCursorColumnError(err error) bool {
	return isMissingAdminAnalyticsColumnError(err, "repair_projection_mask", "repair_phase")
}

func isMissingAdminAnalyticsRepairColumnError(err error) bool {
	return isMissingAdminAnalyticsColumnError(err, "repair_status")
}

// isMissingAdminAnalyticsStructureError reports that the checkpoint table, or one
// of the additive analytic columns the readers depend on, is absent. Such a gap is
// an environment condition rather than a query defect: callers must degrade the
// section through errAdminAnalyticsProjectionUnavailable instead of failing with a
// raw MySQL error, which would otherwise look like a broken dashboard section
// while the real cause is an out-of-date schema.
func isMissingAdminAnalyticsStructureError(err error) bool {
	return isMissingTableError(err) ||
		isMissingAdminAnalyticsCursorColumnError(err) ||
		isMissingAdminAnalyticsRepairColumnError(err)
}

func isMissingAdminAnalyticsColumnError(err error, columns ...string) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	if !(strings.Contains(message, "error 1054") || strings.Contains(message, "42s22") || strings.Contains(message, "unknown column")) {
		return false
	}
	for _, column := range columns {
		if strings.Contains(message, strings.ToLower(column)) {
			return true
		}
	}
	return false
}

type adminWorkbenchReadPolicyKey struct{}

func withoutAdminRawLogFallback(ctx context.Context) context.Context {
	return context.WithValue(ctx, adminWorkbenchReadPolicyKey{}, true)
}

func rejectAdminRawLogFallback(ctx context.Context) error {
	if disabled, _ := ctx.Value(adminWorkbenchReadPolicyKey{}).(bool); disabled {
		return errAdminAnalyticsProjectionUnavailable
	}
	return nil
}
