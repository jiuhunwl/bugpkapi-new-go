package analytics

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// The administrator workbench, homepage rankings, and the access atlas only
// consume recent buckets.  Recovery is therefore deliberately bounded to a
// little more than the widest interactive window instead of replaying the
// complete append-only log after every restore or schema migration.
const rollupRepairWindowDays = 31

// Repair tasks are persisted by migration 000090.  Keeping the state on the
// checkpoint row means a restart cannot silently forget that a migration left
// the projections incomplete.
const (
	repairStatusNone     = "NONE"
	repairStatusPending  = "PENDING"
	repairStatusRunning  = "RUNNING"
	repairStatusComplete = "COMPLETE"
	maxRepairWindowDays  = 366
	repairClearBatchSize = 2_000
)

const (
	repairPhaseIdle     = "IDLE"
	repairPhaseClear    = "CLEAR"
	repairPhaseRollup   = "ROLLUP"
	repairPhaseFinalize = "FINALIZE"
)

const (
	projectionMetricMinute = iota
	projectionAPIMinute
	projectionAPIDay
	projectionUserMinute
	projectionRegionIPHour
	projectionRegionHour
	projectionFailureIPHour
	projectionFailureUserHour
	projectionRiskFingerprint
	// projectionMetricDay and projectionUserMetricDay were introduced by
	// migration 000100. They are appended at the end of the block on purpose:
	// every projection bit is derived from its index (projectionRepairBit) and
	// rollupStatements is indexed by the same number, so inserting anywhere else
	// would silently reassign the masks of existing projections.
	projectionMetricDay
	projectionUserMetricDay
	projectionCount
)

type projectionSourceClass uint8

const (
	projectionSourceCommon projectionSourceClass = iota
	projectionSourceFailureWithIP
	projectionSourceFailure
	projectionSourceRisk
)

type repairProjectionDescriptor struct {
	index        int
	name         string
	watermarkCol string
	optional     bool
	sourceClass  projectionSourceClass
	// coverageWindow overrides the probe's default 31-day bound. A projection
	// whose retention is deliberately shorter must state that bound here:
	// otherwise the probe compares a table holding one day against a source
	// covering a month and reports it as permanently incomplete.
	coverageWindow string
	// repairWindowExpr bounds a rebuild of this projection to its own window
	// instead of the shared repair window. It must be set whenever the
	// projection's retention is shorter than the repair window: the resumable
	// repair scans one source chunk for every masked projection, so without this
	// a 24-hour table would be refilled with a month of rows on every repair.
	repairWindowExpr string
}

// repairWindowSQL is the SQL lower bound a repair must respect for this
// projection. An empty repairWindowExpr means the shared, caller-supplied
// window start applies.
func (d repairProjectionDescriptor) repairWindowSQL(days int) string {
	if d.repairWindowExpr != "" {
		return d.repairWindowExpr
	}
	return fmt.Sprintf("date_sub(current_date(), interval %d day)", days)
}

func (d repairProjectionDescriptor) isDailyProjection() bool {
	return d.index == projectionAPIDay || d.index == projectionMetricDay || d.index == projectionUserMetricDay
}

// hasOwnRepairWindow reports whether a rebuild of this projection must be
// bounded by its own expression rather than the shared frozen window start.
func (d repairProjectionDescriptor) hasOwnRepairWindow() bool {
	return d.repairWindowExpr != ""
}

var repairProjectionDescriptors = [...]repairProjectionDescriptor{
	{index: projectionMetricMinute, name: "bp_call_metric_minute", watermarkCol: "bucket_start", sourceClass: projectionSourceCommon},
	{index: projectionAPIMinute, name: "bp_call_api_metric_minute", watermarkCol: "bucket_start", sourceClass: projectionSourceCommon},
	{index: projectionAPIDay, name: "bp_call_api_metric_day", watermarkCol: "bucket_date", optional: true, sourceClass: projectionSourceCommon, repairWindowExpr: dayLayerRebuildWindowSQL},
	{index: projectionUserMinute, name: "bp_call_user_metric_minute", watermarkCol: "bucket_start", sourceClass: projectionSourceCommon},
	{index: projectionRegionIPHour, name: "bp_call_region_ip_hour", watermarkCol: "bucket_start", sourceClass: projectionSourceCommon},
	{index: projectionRegionHour, name: "bp_call_region_hour", watermarkCol: "bucket_start", sourceClass: projectionSourceCommon},
	{index: projectionFailureIPHour, name: "bp_call_failure_ip_hour", watermarkCol: "bucket_start", sourceClass: projectionSourceFailureWithIP},
	{index: projectionFailureUserHour, name: "bp_call_failure_user_hour", watermarkCol: "bucket_start", sourceClass: projectionSourceFailure},
	{index: projectionRiskFingerprint, name: "bp_call_risk_fingerprint_5m", watermarkCol: "bucket_start", sourceClass: projectionSourceRisk, coverageWindow: riskFingerprintWindowSQL, repairWindowExpr: riskFingerprintWindowSQL},
	{index: projectionMetricDay, name: "bp_call_metric_day", watermarkCol: "bucket_date", optional: true, sourceClass: projectionSourceCommon, repairWindowExpr: dayLayerRebuildWindowSQL},
	{index: projectionUserMetricDay, name: "bp_call_user_metric_day", watermarkCol: "bucket_date", optional: true, sourceClass: projectionSourceCommon, repairWindowExpr: dayLayerRebuildWindowSQL},
}

type projectionMetric struct {
	callCount    uint64
	failureCount uint64
	latest       sql.NullTime
	available    bool
}

type rollupRepairCoverage struct {
	sourceLatest         sql.NullTime
	failureWithIPLatest  sql.NullTime
	failureLatest        sql.NullTime
	riskLatest           sql.NullTime
	sourceCoverageReady  bool
	sourceCallCount      uint64
	sourceFailureCount   uint64
	sourceFailureIPCount uint64
	sourceRiskCallCount  uint64
	metrics              [projectionCount]projectionMetric
}

type repairCursorState struct {
	Available       bool
	UpperLogID      int64
	CursorID        int64
	ProjectionMask  uint16
	Phase           string
	WindowStart     sql.NullTime
	ClearProjection int
}

func isMissingRepairCursorColumnError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	if !(strings.Contains(message, "error 1054") || strings.Contains(message, "42s22")) {
		return false
	}
	for _, column := range []string{
		"repair_upper_log_id",
		"repair_cursor_id",
		"repair_projection_mask",
		"repair_phase",
		"repair_window_start",
		"repair_clear_projection",
	} {
		if strings.Contains(message, column) {
			return true
		}
	}
	return false
}

func normalizeRepairPhase(value string) string {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case repairPhaseClear:
		return repairPhaseClear
	case repairPhaseRollup:
		return repairPhaseRollup
	case repairPhaseFinalize:
		return repairPhaseFinalize
	default:
		return repairPhaseIdle
	}
}

func nextRepairProjection(mask uint16, start int) int {
	if start < 0 {
		start = 0
	}
	for index := start; index < projectionCount; index++ {
		if mask&projectionRepairBit(index) != 0 {
			return index
		}
	}
	return projectionCount
}

// maskIsDailyOnly reports that every selected projection is rebuilt from the
// minute layer. Such a repair never needs the raw-log replay, so it can go
// straight to FINALIZE instead of scanning the complete retained log for
// statements that would write nothing.
func maskIsDailyOnly(mask uint16) bool {
	selected := false
	for _, descriptor := range repairProjectionDescriptors {
		if mask&projectionRepairBit(descriptor.index) == 0 {
			continue
		}
		if !descriptor.isDailyProjection() {
			return false
		}
		selected = true
	}
	return selected
}

func (s rollupState) needsPersistedRepair() bool {
	status := strings.ToUpper(strings.TrimSpace(s.RepairStatus))
	return status == repairStatusPending || status == repairStatusRunning
}

func normalizeRepairWindowDays(days int) int {
	if days <= 0 {
		return rollupRepairWindowDays
	}
	if days > maxRepairWindowDays {
		return maxRepairWindowDays
	}
	return days
}

var commonProjectionIndexes = [...]int{
	projectionMetricMinute,
	projectionAPIMinute,
	projectionUserMinute,
	projectionRegionIPHour,
	projectionRegionHour,
}

// A scalar ordered lookup preserves the NULL-on-empty MAX contract while
// stopping at the first eligible index entry. STRAIGHT_JOIN prevents a
// semijoin rewrite from starting with all users and sorting their log history.
const recentCommonSourceLatestQuery = `
	select (
	select straight_join l.created_at
	from bp_interface_call_log l force index(idx_bp_interface_call_log_created_id)
	where l.id<=?
	  and l.created_at>=date_sub(current_date(), interval 31 day)
	  and l.charge_type<>'ADMIN'
	  and exists(select 1 from bp_user u where u.id=l.user_id)
	order by l.created_at desc limit 1
	)
`

var recentFailureWithIPLatestQuery = `
	select (
	select straight_join l.created_at
	from bp_interface_call_log l force index(idx_bp_interface_call_log_success)
	where l.id<=?
	  and l.created_at>=date_sub(current_date(), interval 31 day)
	  and l.charge_type<>'ADMIN'
	  and ` + statsFailureExpr + `
	  and coalesce(l.client_ip,'')<>''
	  and exists(select 1 from bp_user u where u.id=l.user_id)
	order by l.created_at desc limit 1
	)
`

var recentFailureLatestQuery = `
	select (
	select straight_join l.created_at
	from bp_interface_call_log l force index(idx_bp_interface_call_log_success)
	where l.id<=?
	  and l.created_at>=date_sub(current_date(), interval 31 day)
	  and l.charge_type<>'ADMIN'
	  and ` + statsFailureExpr + `
	  and exists(select 1 from bp_user u where u.id=l.user_id)
	order by l.created_at desc limit 1
	)
`

var recentRiskLatestQuery = `
	select (
	select straight_join l.created_at
	from bp_interface_call_log l force index(idx_bp_call_log_risk_window)
	where l.id<=?
	  and l.created_at>=` + riskFingerprintWindowSQL + `
	  and l.charge_type<>'ADMIN'
	  and coalesce(l.request_fingerprint,'')<>''
	  and exists(select 1 from bp_user u where u.id=l.user_id)
	order by l.created_at desc limit 1
	)
`

// recentSourceCoverageQuery is the only raw-log aggregate used by the
// consistency probe. It runs in the background worker, not in an HTTP request,
// and is executed at most once per process audit interval. Comparing this
// source-of-truth cardinality with every projection catches the migration
// failure mode where all projections contain the same incomplete subset.
//
// The risk column is deliberately counted over the contributor table's own
// retention window rather than the 31-day repair window. That table is pruned
// to the detection window, so counting a month of source rows against one day
// of buckets would mark the projection as incomplete forever. Both sides of
// that comparison use riskFingerprintWindowSQL, so they stay in step.
var recentSourceCoverageQuery = `
	select count(*),
	       coalesce(sum(` + statsFailureExpr + `),0),
	       coalesce(sum(` + statsFailureExpr + ` and coalesce(l.client_ip,'')<>''),0),
	       coalesce(sum(l.created_at>=` + riskFingerprintWindowSQL + ` and coalesce(l.request_fingerprint,'')<>''),0)
	from bp_interface_call_log l
	where l.id<=?
	  and l.created_at>=date_sub(current_date(), interval 31 day)
	  and l.charge_type<>'ADMIN'
	  and exists(select 1 from bp_user u where u.id=l.user_id)
`

func projectionRepairBit(index int) uint16 {
	if index < 0 || index >= projectionCount {
		return 0
	}
	return uint16(1) << uint(index)
}

func (s rollupState) needsWindowRepair() bool {
	return s.CheckpointID > 0 &&
		s.NewestLogID > 0 &&
		s.CheckpointID == s.NewestLogID &&
		s.WindowRepairMask != 0
}

// needsCheckpointZeroRepair identifies a restored database whose checkpoint
// was reset to zero even though one or more projections already contain data.
// Replaying the complete source log from zero would double-count those rows.
// The caller must rebuild only the bounded dashboard window and then advance
// the checkpoint to the current source high-water mark.
func (s rollupState) needsCheckpointZeroRepair() bool {
	return s.CheckpointID == 0 &&
		s.NewestLogID > 0 &&
		s.HasEligibleSource &&
		s.HasAnyRollup
}

func allProjectionRepairMask() uint16 {
	var mask uint16
	for _, descriptor := range repairProjectionDescriptors {
		mask |= projectionRepairBit(descriptor.index)
	}
	return mask
}

// datasetFloorQuery reads the first day the current call-log dataset covers.
// The nightly cleanup truncates the log while the day layers keep every
// earlier day, so projections may legitimately hold history the live log can
// neither verify nor rebuild. Every coverage comparison and every repair bound
// must therefore stop at this floor: comparing or clearing anything older
// would report healthy history as drift and then destroy it beyond recovery.
const datasetFloorQuery = `
	select cast(min(created_at) as date)
	from bp_interface_call_log force index(idx_bp_interface_call_log_created_id)
`

// readDatasetFloor returns the dataset floor inside the caller's transaction.
// A NULL floor (empty log) disables the bound; every caller already treats an
// empty source as nothing-to-verify, so the missing bound is unreachable there.
func readDatasetFloor(ctx context.Context, tx *sql.Tx) (sql.NullTime, error) {
	var floor sql.NullTime
	if err := tx.QueryRowContext(ctx, datasetFloorQuery).Scan(&floor); err != nil {
		return sql.NullTime{}, fmt.Errorf("read analytics dataset floor: %w", err)
	}
	return floor, nil
}

// floorBoundExpr wraps a window lower bound with the dataset floor. The bound
// becomes the LATER of the two dates, so a repair or comparison can never
// reach back past what the current call-log dataset contains. The coalesce
// keeps the expression NULL-safe: greatest() returns NULL when any argument is
// NULL, and a missing floor must mean "no extra bound", not "no rows match".
func floorBoundExpr(windowExpr string) string {
	return fmt.Sprintf(
		"greatest(%s, coalesce(?, '1970-01-01'))",
		windowExpr,
	)
}

// probeRecentRollupCoverage compares low-cardinality rollup totals and bucket
// watermarks. It is intentionally called only when the checkpoint is already
// at the source high-water mark and the normal consistency probe is due. The
// destination reads are bounded index scans. When the low-frequency source
// audit is due, recentSourceCoverageQuery also counts the bounded recovery
// window; that aggregate runs only in the background worker and never in an
// admin HTTP request.
//
// Every projection total is compared from the dataset floor forward. Day-layer
// history older than the current call-log dataset (kept by design across the
// nightly truncation) is excluded, so legitimate history can never surface as
// a totals disagreement and trigger a destructive repair.
func probeRecentRollupCoverage(ctx context.Context, tx *sql.Tx, newestLogID int64, includeSourceCoverage bool) (rollupRepairCoverage, error) {
	coverage := rollupRepairCoverage{}
	floor, err := readDatasetFloor(ctx, tx)
	if err != nil {
		return coverage, err
	}
	if includeSourceCoverage {
		if err := tx.QueryRowContext(ctx, recentSourceCoverageQuery, newestLogID).Scan(
			&coverage.sourceCallCount,
			&coverage.sourceFailureCount,
			&coverage.sourceFailureIPCount,
			&coverage.sourceRiskCallCount,
		); err != nil {
			return coverage, fmt.Errorf("probe recent analytics source coverage: %w", err)
		}
		coverage.sourceCoverageReady = true
	}
	watermarks, err := probeRecentSourceWatermarks(ctx, tx, newestLogID)
	if err != nil {
		return coverage, err
	}
	coverage.sourceLatest = watermarks.sourceLatest
	coverage.failureLatest = watermarks.failureLatest
	coverage.failureWithIPLatest = watermarks.failureWithIPLatest
	coverage.riskLatest = watermarks.riskLatest
	return readProjectionCoverageAtFloor(ctx, tx, coverage, floor)
}

func readProjectionCoverage(ctx context.Context, tx *sql.Tx, coverage rollupRepairCoverage) (rollupRepairCoverage, error) {
	floor, err := readDatasetFloor(ctx, tx)
	if err != nil {
		return coverage, err
	}
	return readProjectionCoverageAtFloor(ctx, tx, coverage, floor)
}
func readProjectionCoverageAtFloor(ctx context.Context, tx *sql.Tx, coverage rollupRepairCoverage, floor sql.NullTime) (rollupRepairCoverage, error) {
	if !coverage.sourceLatest.Valid {
		return coverage, nil
	}

	for _, descriptor := range repairProjectionDescriptors {
		query, args := projectionCoverageQuery(descriptor, floor)
		var metric projectionMetric
		if err := tx.QueryRowContext(ctx, query, args...).Scan(
			&metric.callCount,
			&metric.failureCount,
			&metric.latest,
		); err != nil {
			if descriptor.optional && isMissingOptionalRollupTable(err, descriptor.name) {
				continue
			}
			return coverage, fmt.Errorf("probe %s coverage: %w", descriptor.name, err)
		}
		metric.available = true
		coverage.metrics[descriptor.index] = metric
	}
	return coverage, nil
}

// projectionCoverageQuery builds one projection-side coverage aggregate. The
// comparison window is floored at the current dataset start: rows older than
// the call log's oldest row are history the source can never confirm, so they
// are excluded instead of being reported as missing coverage.
func projectionCoverageQuery(descriptor repairProjectionDescriptor, floor sql.NullTime) (string, []any) {
	watermark := descriptor.watermarkCol
	if watermark == "bucket_date" {
		watermark = "cast(max(bucket_date) as datetime)"
	} else {
		watermark = "max(" + watermark + ")"
	}
	callTotal := "call_count"
	if descriptor.sourceClass == projectionSourceFailure || descriptor.sourceClass == projectionSourceFailureWithIP {
		// Failure projections intentionally store only failure_count and do not
		// have a call_count column.
		callTotal = "failure_count"
	}
	window := descriptor.coverageWindow
	if window == "" {
		window = fmt.Sprintf("date_sub(current_date(), interval %d day)", rollupRepairWindowDays)
	}
	query := fmt.Sprintf(
		"select coalesce(sum(%s),0), coalesce(sum(failure_count),0), %s from %s where %s>=%s",
		callTotal,
		watermark,
		descriptor.name,
		descriptor.watermarkCol,
		floorBoundExpr(window),
	)
	return query, []any{floor}
}

func (c rollupRepairCoverage) repairMask() uint16 {
	if !c.sourceLatest.Valid {
		return 0
	}

	var mask uint16

	// These projections have exactly the same source eligibility and therefore
	// must carry the same totals for the repair window. Never choose the
	// largest value as a reference: a duplicated projection could otherwise be
	// treated as healthy and leave every other projection permanently behind.
	// When the common totals disagree (or are all zero/unavailable), rebuild the
	// complete common set so the window is restored from one source of truth.
	commonTotalsConsistent := true
	commonTotalsMatchSource := true
	var referenceCalls, referenceFailures uint64
	referenceSet := false
	for _, index := range commonProjectionIndexes {
		metric := c.metrics[index]
		if !metric.available {
			commonTotalsConsistent = false
			continue
		}
		if !referenceSet {
			referenceCalls = metric.callCount
			referenceFailures = metric.failureCount
			referenceSet = true
			continue
		}
		if metric.callCount != referenceCalls || metric.failureCount != referenceFailures {
			commonTotalsConsistent = false
		}
	}
	if c.sourceCoverageReady {
		commonTotalsMatchSource = referenceSet &&
			referenceCalls == c.sourceCallCount &&
			referenceFailures == c.sourceFailureCount
	}
	if !referenceSet || referenceCalls == 0 || !commonTotalsConsistent || !commonTotalsMatchSource {
		for _, index := range commonProjectionIndexes {
			mask |= projectionRepairBit(index)
		}
	} else {
		for _, index := range commonProjectionIndexes {
			metric := c.metrics[index]
			bucketWidth := time.Minute
			if index == projectionRegionIPHour || index == projectionRegionHour {
				bucketWidth = time.Hour
			}
			behind := !metric.latest.Valid ||
				rollupBucketBefore(metric.latest.Time, c.sourceLatest.Time, bucketWidth)
			if behind {
				mask |= projectionRepairBit(index)
			}
		}
	}

	// The daily API projection mirrors API-minute calls over the same 31-day
	// window. It is optional while old databases are upgrading, so an absent
	// table is intentionally not marked for repair.
	daily := c.metrics[projectionAPIDay]
	apiMinute := c.metrics[projectionAPIMinute]
	if daily.available {
		if !commonTotalsConsistent || !commonTotalsMatchSource || !referenceSet || referenceCalls == 0 ||
			!apiMinute.available || daily.callCount != apiMinute.callCount ||
			!daily.latest.Valid || rollupBucketBefore(daily.latest.Time, c.sourceLatest.Time, 24*time.Hour) {
			mask |= projectionRepairBit(projectionAPIDay)
		}
	}

	// The day layer aggregates exactly the same eligible source rows as the
	// common minute projections, so when it is present its 31-day totals and its
	// day-width watermark must agree with them. It is deliberately kept out of
	// commonProjectionIndexes: those indexes must be present for the shared
	// reference to exist, and an optional table that is merely absent would
	// otherwise mark every common projection for repair on every probe.
	for _, index := range [...]int{projectionMetricDay, projectionUserMetricDay} {
		metric := c.metrics[index]
		if !metric.available {
			continue
		}
		if !commonTotalsConsistent || !commonTotalsMatchSource || !referenceSet || referenceCalls == 0 ||
			metric.callCount != referenceCalls || metric.failureCount != referenceFailures ||
			!metric.latest.Valid || rollupBucketBefore(metric.latest.Time, c.sourceLatest.Time, 24*time.Hour) {
			mask |= projectionRepairBit(index)
		}
	}

	if c.failureLatest.Valid {
		failureUser := c.metrics[projectionFailureUserHour]
		overall := c.metrics[projectionMetricMinute]
		failureNeedsRepair := !failureUser.available ||
			failureUser.failureCount != overall.failureCount ||
			!failureUser.latest.Valid ||
			rollupBucketBefore(failureUser.latest.Time, c.failureLatest.Time, time.Hour)
		if c.sourceCoverageReady && failureUser.failureCount != c.sourceFailureCount {
			failureNeedsRepair = true
		}
		if failureNeedsRepair {
			mask |= projectionRepairBit(projectionFailureUserHour)
		}
	}
	if c.failureWithIPLatest.Valid {
		failureIP := c.metrics[projectionFailureIPHour]
		if !failureIP.available || !failureIP.latest.Valid ||
			rollupBucketBefore(failureIP.latest.Time, c.failureWithIPLatest.Time, time.Hour) {
			mask |= projectionRepairBit(projectionFailureIPHour)
		} else if c.sourceCoverageReady && failureIP.failureCount != c.sourceFailureIPCount {
			mask |= projectionRepairBit(projectionFailureIPHour)
		}
	}
	if c.riskLatest.Valid {
		risk := c.metrics[projectionRiskFingerprint]
		// The projection's row count is deliberately NOT compared with the raw
		// source count. The table is retained for a fixed window and its rows are
		// floored to five-minute buckets, so a window boundary always straddles a
		// partial bucket: the table holds the whole boundary bucket while the
		// source count sees only the rows after the boundary. That difference is
		// a few thousand rows and would flap the projection into repair on every
		// probe. The watermark is the reliable signal here - it detects both a
		// stalled projection and an empty one, because an empty table never
		// carries a valid watermark.
		if !risk.available || !risk.latest.Valid ||
			rollupBucketBefore(risk.latest.Time, c.riskLatest.Time, 5*time.Minute) {
			mask |= projectionRepairBit(projectionRiskFingerprint)
		}
	}
	return mask
}

func rollupBucketBefore(destination, source time.Time, bucket time.Duration) bool {
	expected := source
	switch bucket {
	case 24 * time.Hour:
		expected = time.Date(source.Year(), source.Month(), source.Day(), 0, 0, 0, 0, source.Location())
	case time.Hour:
		expected = time.Date(source.Year(), source.Month(), source.Day(), source.Hour(), 0, 0, 0, source.Location())
	case 5 * time.Minute:
		expected = time.Date(source.Year(), source.Month(), source.Day(), source.Hour(), source.Minute()/5*5, 0, 0, source.Location())
	case time.Minute:
		expected = time.Date(source.Year(), source.Month(), source.Day(), source.Hour(), source.Minute(), 0, 0, source.Location())
	default:
		expected = source.Truncate(bucket)
	}
	return destination.Before(expected)
}

// repairRecentProjections replaces only the projections identified by the
// coverage probe.  Rows in the repair window are deleted first, so the normal
// additive upserts can be reused without double counting.  The source range
// is capped by newestLogID; new gateway rows inserted during recovery remain
// for the next ordinary checkpoint batch.
//
// Every delete and rebuild bound is floored at the current dataset start: the
// nightly cleanup truncates the call log while day layers keep their history,
// so a repair must never clear (or rebuild over) days the live log cannot
// replay. The daily projections are deliberately rebuilt from their minute
// projections instead: the minute layer retains the complete recent day even
// after the log has been truncated mid-day, so a log replay would certify a
// short day and break the seven-day trend.
func (r *Repository) repairRecentProjections(ctx context.Context, tx *sql.Tx, mask uint16, newestLogID, windowDays int64) error {
	if mask == 0 || newestLogID <= 0 {
		return nil
	}
	floor, err := readReplayFloor(ctx, tx)
	if err != nil {
		return err
	}
	days := normalizeRepairWindowDays(int(windowDays))
	for _, descriptor := range repairProjectionDescriptors {
		if mask&projectionRepairBit(descriptor.index) == 0 || descriptor.isDailyProjection() {
			continue
		}
		windowExpr := floorBoundExpr(descriptor.repairWindowSQL(days))
		deleteQuery := fmt.Sprintf(
			"delete from %s where %s>=%s",
			descriptor.name,
			descriptor.watermarkCol,
			windowExpr,
		)
		if _, err := tx.ExecContext(ctx, deleteQuery, floor); err != nil {
			if descriptor.optional && isMissingOptionalRollupTable(err, descriptor.name) {
				continue
			}
			return fmt.Errorf("clear %s repair window: %w", descriptor.name, err)
		}
		statement, err := legacyBoundedRepairStatement(rollupStatements()[descriptor.index], descriptor.repairWindowSQL(days))
		if err != nil {
			return fmt.Errorf("prepare %s repair: %w", descriptor.name, err)
		}
		if _, err := tx.ExecContext(ctx, statement, floor, newestLogID); err != nil {
			if descriptor.optional && isMissingOptionalRollupTable(err, descriptor.name) {
				continue
			}
			return fmt.Errorf("rebuild %s repair window: %w", descriptor.name, err)
		}
	}
	return r.rebuildDailyProjections(ctx, tx, mask)
}

// initializeResumableRepair freezes both ends of the rebuild. New gateway rows
// above UpperLogID are intentionally left for the ordinary incremental worker,
// while WindowStart never moves when a retry crosses midnight. The source
// cursor is initialized just before the smallest recent id so a primary-key
// chunk scan does not walk the complete historical log.
func (r *Repository) initializeResumableRepair(
	ctx context.Context,
	tx *sql.Tx,
	state repairCursorState,
	newestLogID int64,
	windowDays int64,
	newRequest bool,
) (repairCursorState, error) {
	days := normalizeRepairWindowDays(int(windowDays))
	mask := state.ProjectionMask
	if mask == 0 {
		mask = allProjectionRepairMask()
	}

	var windowStart time.Time
	windowQuery := fmt.Sprintf("select date_sub(current_date(), interval %d day)", days)
	if err := tx.QueryRowContext(ctx, windowQuery).Scan(&windowStart); err != nil {
		return state, fmt.Errorf("freeze analytics repair window: %w", err)
	}
	// A repair must never reach back past the current call-log dataset: the
	// external retention sweep truncates the log mid-day, so clearing over the
	// shared window would destroy buckets the source can no longer replay. The
	// floor is the exact first surviving row, rounded up to the next full
	// minute: the partially covered minute at the truncation boundary keeps the
	// calls the projection already counted instead of being deleted and then
	// rebuilt from a log that no longer holds them.
	floor, err := readReplayFloor(ctx, tx)
	if err != nil {
		return state, err
	}
	if floored := floorBoundedWindowStart(windowStart, floor); floored.After(windowStart) {
		windowStart = floored
	}

	cursorID := newestLogID
	if newestLogID > 0 {
		var minimumID sql.NullInt64
		if err := tx.QueryRowContext(ctx, `
			select min(id)
			from bp_interface_call_log force index(idx_bp_interface_call_log_created_id)
			where created_at>=? and id<=?
		`, windowStart, newestLogID).Scan(&minimumID); err != nil {
			return state, fmt.Errorf("locate analytics repair source start: %w", err)
		}
		if minimumID.Valid && minimumID.Int64 > 0 {
			cursorID = minimumID.Int64 - 1
		}
	}

	// A repair that only touches the daily projections is rebuilt from the
	// minute layer in FINALIZE, so it has no log chunk to replay. Starting it at
	// FINALIZE avoids scanning the complete retained log for statements that
	// would write nothing.
	phase := repairPhaseClear
	if maskIsDailyOnly(mask) {
		phase = repairPhaseFinalize
		cursorID = newestLogID
	}

	requestTimestamp := "repair_requested_at"
	if newRequest {
		requestTimestamp = "now(6)"
	}
	statement := fmt.Sprintf(`
		update bp_analytics_checkpoint
		set repair_status=?,repair_window_days=?,repair_requested_at=%s,
		    repair_started_at=coalesce(repair_started_at,now(6)),repair_completed_at=null,
		    repair_upper_log_id=?,repair_cursor_id=?,repair_projection_mask=?,
		    repair_phase=?,repair_window_start=?,repair_clear_projection=0,last_error=''
		where worker_key=?
	`, requestTimestamp)
	if _, err := tx.ExecContext(
		ctx,
		statement,
		repairStatusRunning,
		days,
		newestLogID,
		cursorID,
		mask,
		phase,
		windowStart,
		CheckpointKey,
	); err != nil {
		return state, fmt.Errorf("initialize resumable analytics repair: %w", err)
	}

	return repairCursorState{
		Available:       true,
		UpperLogID:      newestLogID,
		CursorID:        cursorID,
		ProjectionMask:  mask,
		Phase:           phase,
		WindowStart:     sql.NullTime{Time: windowStart, Valid: true},
		ClearProjection: 0,
	}, nil
}

// floorBoundedWindowStart returns the later of a requested window start and
// the exact replay floor. The floor is rounded up to the next whole minute
// because a minute bucket that the surviving log only partially covers cannot
// be rebuilt exactly: clearing it would drop calls that the projection already
// counted. Projections may hold history older than the live call log, so a
// repair must never start before the first fully replayable bucket.
func floorBoundedWindowStart(windowStart time.Time, floor sql.NullTime) time.Time {
	if !floor.Valid {
		return windowStart
	}
	exact := ceilMinute(floor.Time)
	if exact.After(windowStart) {
		return exact
	}
	return windowStart
}

// ceilMinute rounds a timestamp up to the next whole minute. A timestamp that
// already sits on a minute boundary is returned unchanged.
func ceilMinute(value time.Time) time.Time {
	truncated := value.Truncate(time.Minute)
	if truncated.Equal(value) {
		return value
	}
	return truncated.Add(time.Minute)
}

// readReplayFloor returns the exact creation time of the oldest call-log row.
// It bounds every destructive repair step: rows older than this are no longer
// in the source, so no projection may be cleared over them.
func readReplayFloor(ctx context.Context, tx *sql.Tx) (sql.NullTime, error) {
	var floor sql.NullTime
	if err := tx.QueryRowContext(ctx, `
		select min(created_at)
		from bp_interface_call_log force index(idx_bp_interface_call_log_created_id)
	`).Scan(&floor); err != nil {
		return sql.NullTime{}, fmt.Errorf("read analytics replay floor: %w", err)
	}
	return floor, nil
}

func (r *Repository) scheduleResumableRepair(
	ctx context.Context,
	tx *sql.Tx,
	mask uint16,
	newestLogID int64,
	windowDays int64,
) (BatchResult, error) {
	state, err := r.initializeResumableRepair(
		ctx,
		tx,
		repairCursorState{Available: true, ProjectionMask: mask},
		newestLogID,
		windowDays,
		true,
	)
	if err != nil {
		return BatchResult{}, err
	}
	return BatchResult{FromLogID: state.CursorID, ToLogID: state.CursorID, HasMore: true}, nil
}

// processResumableRepairStep performs at most one bounded delete or one source
// chunk per call. Its caller commits the checkpoint mutation and projection
// writes together, which makes restarting at repair_cursor_id idempotent.
func (r *Repository) processResumableRepairStep(
	ctx context.Context,
	tx *sql.Tx,
	state repairCursorState,
	newestLogID int64,
	windowDays int64,
	batchSize int,
) (BatchResult, error) {
	phase := normalizeRepairPhase(state.Phase)
	if !state.WindowStart.Valid || phase == repairPhaseIdle || state.ProjectionMask == 0 {
		var err error
		state, err = r.initializeResumableRepair(ctx, tx, state, newestLogID, windowDays, false)
		if err != nil {
			return BatchResult{}, err
		}
		phase = repairPhaseClear
	}
	// The exact replay floor bounds every per-projection window: a projection
	// whose own retention expression lags behind the current dataset (a freshly
	// truncated log) must never clear or rebuild rows the log cannot replay.
	floor, err := readReplayFloor(ctx, tx)
	if err != nil {
		return BatchResult{}, err
	}

	switch phase {
	case repairPhaseClear:
		index := nextRepairProjection(state.ProjectionMask, state.ClearProjection)
		if index >= projectionCount {
			if _, err := tx.ExecContext(ctx, `
				update bp_analytics_checkpoint
				set repair_phase=?,repair_clear_projection=?,last_batch_size=0,last_error=''
				where worker_key=?
			`, repairPhaseRollup, projectionCount, CheckpointKey); err != nil {
				return BatchResult{}, fmt.Errorf("advance analytics repair to rollup: %w", err)
			}
			return BatchResult{FromLogID: state.CursorID, ToLogID: state.CursorID, HasMore: true}, nil
		}

		descriptor := repairProjectionDescriptors[index]
		deleted := int64(0)
		if !descriptor.isDailyProjection() {
			// Daily projections are cleared and rebuilt atomically from their
			// minute projections in FINALIZE. Clearing them here would either
			// delete history the minute layer no longer covers or leave the
			// seven-day trend empty between steps and a midnight rollover.
			deleteQuery, deleteArgs := repairClearChunk(descriptor, state.WindowStart.Time, floor, repairClearBatchSize)
			deleteResult, err := tx.ExecContext(ctx, deleteQuery, deleteArgs...)
			if err != nil {
				if !descriptor.optional || !isMissingOptionalRollupTable(err, descriptor.name) {
					return BatchResult{}, fmt.Errorf("clear %s repair chunk: %w", descriptor.name, err)
				}
			} else if deleted, err = deleteResult.RowsAffected(); err != nil {
				return BatchResult{}, fmt.Errorf("count cleared %s repair rows: %w", descriptor.name, err)
			}
		}

		next := index
		if descriptor.isDailyProjection() || deleted < repairClearBatchSize {
			next = nextRepairProjection(state.ProjectionMask, index+1)
		}
		nextPhase := repairPhaseClear
		if next >= projectionCount {
			nextPhase = repairPhaseRollup
		}
		if _, err := tx.ExecContext(ctx, `
			update bp_analytics_checkpoint
			set repair_phase=?,repair_clear_projection=?,last_batch_size=0,last_error=''
			where worker_key=?
		`, nextPhase, next, CheckpointKey); err != nil {
			return BatchResult{}, fmt.Errorf("advance analytics repair clear cursor: %w", err)
		}
		return BatchResult{FromLogID: state.CursorID, ToLogID: state.CursorID, HasMore: true}, nil

	case repairPhaseRollup:
		if state.CursorID >= state.UpperLogID {
			if _, err := tx.ExecContext(ctx, `
				update bp_analytics_checkpoint
				set repair_phase=?,repair_cursor_id=repair_upper_log_id,last_batch_size=0,last_error=''
				where worker_key=?
			`, repairPhaseFinalize, CheckpointKey); err != nil {
				return BatchResult{}, fmt.Errorf("advance analytics repair to finalize: %w", err)
			}
			return BatchResult{FromLogID: state.CursorID, ToLogID: state.CursorID, HasMore: true}, nil
		}

		var toID int64
		var sourceRows int
		var lastCreatedAt sql.NullTime
		if err := tx.QueryRowContext(ctx, `
			select coalesce(max(id),0),count(*),max(created_at)
			from (
				select id,created_at
				from bp_interface_call_log force index(primary)
				where id>? and id<=? and created_at>=?
				order by id
				limit ?
			) source_window
		`, state.CursorID, state.UpperLogID, state.WindowStart.Time, normalizeBatchSize(batchSize)).Scan(
			&toID,
			&sourceRows,
			&lastCreatedAt,
		); err != nil {
			return BatchResult{}, fmt.Errorf("select analytics repair source chunk: %w", err)
		}
		if sourceRows == 0 {
			if _, err := tx.ExecContext(ctx, `
				update bp_analytics_checkpoint
				set repair_phase=?,repair_cursor_id=repair_upper_log_id,last_batch_size=0,last_error=''
				where worker_key=?
			`, repairPhaseFinalize, CheckpointKey); err != nil {
				return BatchResult{}, fmt.Errorf("finish analytics repair source scan: %w", err)
			}
			return BatchResult{FromLogID: state.CursorID, ToLogID: state.UpperLogID, HasMore: true}, nil
		}

		for _, descriptor := range repairProjectionDescriptors {
			if state.ProjectionMask&projectionRepairBit(descriptor.index) == 0 || descriptor.isDailyProjection() {
				// Daily projections are rebuilt from the minute layer in FINALIZE;
				// replaying the surviving log would under-count the truncated first
				// day and permanently certify a short daily trend.
				continue
			}
			statement, args, err := repairRollupChunk(descriptor, state.CursorID, toID, state.WindowStart.Time, floor)
			if err != nil {
				return BatchResult{}, fmt.Errorf("prepare %s repair chunk: %w", descriptor.name, err)
			}
			if _, err := tx.ExecContext(ctx, statement, args...); err != nil {
				if descriptor.optional && isMissingOptionalRollupTable(err, descriptor.name) {
					continue
				}
				return BatchResult{}, fmt.Errorf("rebuild %s repair chunk: %w", descriptor.name, err)
			}
		}
		contributionRows, err := countRepairContributions(ctx, tx, state.CursorID, toID, state.WindowStart.Time)
		if err != nil {
			return BatchResult{}, err
		}
		if _, err := tx.ExecContext(ctx, `
			update bp_analytics_checkpoint
			set repair_cursor_id=?,last_batch_size=?,last_completed_at=now(6),last_error=''
			where worker_key=?
		`, toID, sourceRows, CheckpointKey); err != nil {
			return BatchResult{}, fmt.Errorf("advance analytics repair source cursor: %w", err)
		}
		return BatchResult{
			FromLogID:        state.CursorID,
			ToLogID:          toID,
			SourceRows:       sourceRows,
			ContributionRows: contributionRows,
			HasMore:          true,
		}, nil

	case repairPhaseFinalize:
		// The daily projections are recomputed from their minute projections in
		// the same transaction that marks the repair complete. The minute layer
		// retains complete recent days even after the raw log was truncated, so
		// this is the only replay source that can certify an exact day. It is
		// idempotent: the window floor is frozen once per call and both the
		// delete and the rebuild use it, so a retried step cannot leave a hole.
		if err := r.rebuildDailyProjections(ctx, tx, state.ProjectionMask); err != nil {
			return BatchResult{}, err
		}
		var lastCreatedAt sql.NullTime
		if state.UpperLogID > 0 {
			err := tx.QueryRowContext(ctx, `
				select created_at from bp_interface_call_log where id=?
			`, state.UpperLogID).Scan(&lastCreatedAt)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return BatchResult{}, fmt.Errorf("read analytics repair high-water time: %w", err)
			}
		}
		if _, err := tx.ExecContext(ctx, `
			update bp_analytics_checkpoint
			set last_log_id=?,last_log_created_at=?,last_batch_size=0,last_completed_at=now(6),
			    last_error='',repair_status=?,repair_phase=?,repair_cursor_id=?,
			    repair_clear_projection=?,repair_completed_at=now(6)
			where worker_key=?
		`,
			state.UpperLogID,
			lastCreatedAt,
			repairStatusComplete,
			repairPhaseIdle,
			state.UpperLogID,
			projectionCount,
			CheckpointKey,
		); err != nil {
			return BatchResult{}, fmt.Errorf("finalize resumable analytics repair: %w", err)
		}
		return BatchResult{FromLogID: state.CursorID, ToLogID: state.UpperLogID}, nil
	}

	return BatchResult{}, fmt.Errorf("unsupported analytics repair phase %q", state.Phase)
}

func countRepairContributions(ctx context.Context, tx *sql.Tx, fromID, toID int64, windowStart time.Time) (int, error) {
	var count int
	if err := tx.QueryRowContext(ctx, `
		select count(*)
		from bp_interface_call_log l
		where l.id>? and l.id<=? and l.created_at>=?
		  and l.charge_type<>'ADMIN'
		  and exists(select 1 from bp_user u where u.id=l.user_id)
	`, fromID, toID, windowStart).Scan(&count); err != nil {
		return 0, fmt.Errorf("count analytics repair contributions: %w", err)
	}
	return count, nil
}

// The daily projections are the only durable history that survives the raw
// call log. Their repair must therefore never depend on the log: the external
// retention sweep truncates it mid-day, so a log replay would rebuild the
// first day from the truncation point onward and certify a short trend. The
// minute projections keep complete recent days, so the day layer is recomputed
// from them instead. Every statement replaces existing rows (VALUES) rather
// than accumulating, which makes a retried FINALIZE idempotent.
var dailyProjectionRebuildStatements = map[int]string{
	projectionMetricDay: `
	insert into bp_call_metric_day(
		bucket_date,call_count,success_count,failure_count,billable_count,
		charge_amount,point_amount,request_bytes,response_bytes,elapsed_sum,
		elapsed_min,elapsed_max,first_seen,last_seen
	)
	select date(bucket_start),sum(call_count),sum(success_count),sum(failure_count),sum(billable_count),
	       sum(charge_amount),sum(point_amount),sum(request_bytes),sum(response_bytes),sum(elapsed_sum),
	       min(elapsed_min),max(elapsed_max),min(first_seen),max(last_seen)
	from bp_call_metric_minute
	where bucket_start>=?
	group by date(bucket_start)
	on duplicate key update
		call_count=values(call_count),success_count=values(success_count),failure_count=values(failure_count),
		billable_count=values(billable_count),charge_amount=values(charge_amount),point_amount=values(point_amount),
		request_bytes=values(request_bytes),response_bytes=values(response_bytes),elapsed_sum=values(elapsed_sum),
		elapsed_min=values(elapsed_min),elapsed_max=values(elapsed_max),
		first_seen=values(first_seen),last_seen=values(last_seen)`,
	projectionUserMetricDay: `
	insert into bp_call_user_metric_day(
		bucket_date,user_id,call_count,success_count,failure_count,billable_count,
		charge_amount,point_amount,request_bytes,response_bytes,elapsed_sum,
		elapsed_min,elapsed_max,first_seen,last_seen
	)
	select date(bucket_start),user_id,sum(call_count),sum(success_count),sum(failure_count),sum(billable_count),
	       sum(charge_amount),sum(point_amount),sum(request_bytes),sum(response_bytes),sum(elapsed_sum),
	       min(elapsed_min),max(elapsed_max),min(first_seen),max(last_seen)
	from bp_call_user_metric_minute
	where bucket_start>=?
	group by date(bucket_start),user_id
	on duplicate key update
		call_count=values(call_count),success_count=values(success_count),failure_count=values(failure_count),
		billable_count=values(billable_count),charge_amount=values(charge_amount),point_amount=values(point_amount),
		request_bytes=values(request_bytes),response_bytes=values(response_bytes),elapsed_sum=values(elapsed_sum),
		elapsed_min=values(elapsed_min),elapsed_max=values(elapsed_max),
		first_seen=values(first_seen),last_seen=values(last_seen)`,
	projectionAPIDay: `
	insert into bp_call_api_metric_day(
		bucket_date,interface_id,user_id,api_code,call_count,success_count,failure_count,
		elapsed_sum,first_seen,last_seen
	)
	select date(bucket_start),interface_id,user_id,max(api_code),sum(call_count),sum(success_count),
	       sum(failure_count),sum(elapsed_sum),min(first_seen),max(last_seen)
	from bp_call_api_metric_minute
	where bucket_start>=?
	group by date(bucket_start),interface_id,user_id
	on duplicate key update
		api_code=values(api_code),call_count=values(call_count),success_count=values(success_count),
		failure_count=values(failure_count),elapsed_sum=values(elapsed_sum),
		first_seen=values(first_seen),last_seen=values(last_seen)`,
}

// rebuildDailyProjections replaces the daily projections selected by mask with
// an exact aggregate of their minute counterparts over the minute layer's
// retention window. Both the clear and the rebuild bind one frozen floor, so
// the pair is atomic inside the caller's transaction and a retry is safe.
func (r *Repository) rebuildDailyProjections(ctx context.Context, tx *sql.Tx, mask uint16) error {
	indexes := [...]int{projectionMetricDay, projectionUserMetricDay, projectionAPIDay}
	selected := false
	for _, index := range indexes {
		if mask&projectionRepairBit(index) != 0 {
			selected = true
			break
		}
	}
	if !selected {
		return nil
	}
	var floor time.Time
	if err := tx.QueryRowContext(ctx, "select "+dayLayerRebuildWindowSQL).Scan(&floor); err != nil {
		return fmt.Errorf("freeze daily projection rebuild window: %w", err)
	}
	for _, index := range indexes {
		if mask&projectionRepairBit(index) == 0 {
			continue
		}
		descriptor := repairProjectionDescriptors[index]
		deleteQuery := fmt.Sprintf(
			"delete from %s where %s>=?",
			descriptor.name,
			descriptor.watermarkCol,
		)
		if _, err := tx.ExecContext(ctx, deleteQuery, floor); err != nil {
			if descriptor.optional && isMissingOptionalRollupTable(err, descriptor.name) {
				continue
			}
			return fmt.Errorf("clear %s daily rebuild window: %w", descriptor.name, err)
		}
		if _, err := tx.ExecContext(ctx, dailyProjectionRebuildStatements[index], floor); err != nil {
			if descriptor.optional && isMissingOptionalRollupTable(err, descriptor.name) {
				continue
			}
			return fmt.Errorf("rebuild %s from its minute projection: %w", descriptor.name, err)
		}
	}
	return nil
}

const rollupSourceRangePredicate = "l.id>? and l.id<=?"

// boundedRepairStatement converts a normal additive rollup into the resumable
// repair form. The caller supplies (cursorID, chunkUpperID, windowStart); both
// the source cursor and the time boundary are persisted in the checkpoint and
// committed in the same transaction as the upserts.
// repairSourceRange builds the predicate that replaces the rollup's id window
// during a repair. An empty windowExpr keeps the historical form, where the
// caller binds the frozen window start (floored at initialization); a non-empty
// one is inlined so that a projection with a shorter retention is only ever
// rebuilt over its own window even though the shared source scan is wider.
// Both forms are floored at the current dataset start so a rebuild can never
// consume source rows older than the log can replay into cleared buckets.
func repairSourceRange(windowExpr string) string {
	if windowExpr == "" {
		return rollupSourceRangePredicate + " and l.created_at>=?"
	}
	return rollupSourceRangePredicate + " and l.created_at>=" + floorBoundExpr(windowExpr)
}

func boundedRepairStatement(statement, windowExpr string) (string, error) {
	if strings.Count(statement, rollupSourceRangePredicate) != 1 {
		return "", fmt.Errorf("rollup statement has unexpected source range")
	}
	return strings.Replace(statement, rollupSourceRangePredicate, repairSourceRange(windowExpr), 1), nil
}

// repairClearChunk builds the bounded delete for one repair chunk together with
// the arguments it must be executed with.
//
// A projection that carries its own retention window inlines that expression,
// floored at the current dataset start; every other projection binds the
// window start frozen in the checkpoint (already floored when the repair was
// initialized), so a retried chunk can never move the boundary. Building the
// SQL and its arguments in one place is what keeps the two from drifting
// apart: a statement executed with an argument it does not declare is rejected
// by the driver before a single row is deleted, which is how a repair used to
// fail on its first step and then retry forever without touching the
// projections.
func repairClearChunk(descriptor repairProjectionDescriptor, windowStart time.Time, floor sql.NullTime, batchSize int) (string, []any) {
	windowExpr := "?"
	args := []any{windowStart}
	if descriptor.hasOwnRepairWindow() && !descriptor.isDailyProjection() {
		// The own retention expression moves with the clock, so it must be
		// floored at the current dataset start at execution time; the frozen
		// shared start was already floored when the repair was initialized.
		windowExpr = floorBoundExpr(descriptor.repairWindowExpr)
		args = []any{floor}
	}
	query := fmt.Sprintf(
		"delete from %s where %s>=%s order by %s limit %d",
		descriptor.name,
		descriptor.watermarkCol,
		windowExpr,
		descriptor.watermarkCol,
		batchSize,
	)
	return query, args
}

// repairRollupChunk builds one bounded rebuild statement for the repair cursor
// range together with the arguments it declares, following the same rule as
// repairClearChunk: the window is a placeholder (paired with the dataset
// floor) unless the projection carries its own, which is inlined but still
// floored.
func repairRollupChunk(descriptor repairProjectionDescriptor, cursorID, chunkUpperID int64, windowStart time.Time, floor sql.NullTime) (string, []any, error) {
	windowExpr := descriptor.repairWindowExpr
	if descriptor.isDailyProjection() {
		windowExpr = ""
	}
	statement, err := boundedRepairStatement(rollupStatements()[descriptor.index], windowExpr)
	if err != nil {
		return "", nil, err
	}
	args := []any{cursorID, chunkUpperID}
	if descriptor.hasOwnRepairWindow() && !descriptor.isDailyProjection() {
		args = append(args, floor)
	} else {
		args = append(args, windowStart)
	}
	return statement, args, nil
}

// legacyBoundedRepairStatement preserves the migration-090 one-transaction
// repair path for binaries deliberately run against a schema that has not yet
// installed migration 000092. Current schemas use boundedRepairStatement and
// the durable cursor state instead.
//
// The inlined window is floored at the current dataset start, and the caller
// must bind the floor before the source high-water id (placeholder order:
// floor, newestLogID).
func legacyBoundedRepairStatement(statement, windowExpr string) (string, error) {
	if strings.Count(statement, rollupSourceRangePredicate) != 1 {
		return "", fmt.Errorf("rollup statement has unexpected source range")
	}
	replacement := "l.created_at>=" + floorBoundExpr(windowExpr) + " and l.id<=?"
	return strings.Replace(statement, rollupSourceRangePredicate, replacement, 1), nil
}
