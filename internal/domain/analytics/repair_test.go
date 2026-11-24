package analytics

import (
	"database/sql"
	"strings"
	"testing"
	"time"
)

func TestBoundedRepairStatementRetainsCursorRangeAndUsesPersistedWindow(t *testing.T) {
	statement, err := boundedRepairStatement(overallMinuteUpsert, "")
	if err != nil {
		t.Fatal(err)
	}
	normalized := strings.ToLower(statement)
	if !strings.Contains(normalized, "l.id>? and l.id<=? and l.created_at>=?") {
		t.Fatalf("repair statement must be source-chunk bounded with a stable persisted window: %s", statement)
	}
	if strings.Contains(normalized, "current_date()") || strings.Contains(normalized, "interval 31 day") {
		t.Fatalf("repair statement must not move its time window between retries: %s", statement)
	}
}

// A projection whose retention is shorter than the shared repair window must
// inline its own bound. Otherwise the resumable repair, which scans one source
// chunk for every masked projection, would refill a 24-hour table with a month
// of rows on every repair.
func TestRepairStatementInlinesOwnWindowInsteadOfBindingSharedOne(t *testing.T) {
	riskDescriptor := repairProjectionDescriptors[projectionRiskFingerprint]
	if !riskDescriptor.hasOwnRepairWindow() {
		t.Fatal("the 24h risk projection must carry its own repair window")
	}
	statement, err := boundedRepairStatement(riskFingerprintUpsert(), riskDescriptor.repairWindowSQL(rollupRepairWindowDays))
	if err != nil {
		t.Fatal(err)
	}
	normalized := strings.ToLower(statement)
	if !strings.Contains(normalized, "l.created_at>=greatest("+strings.ToLower(riskFingerprintWindowSQL)+", coalesce(?, '1970-01-01'))") {
		t.Fatalf("risk repair must inline its retention window floored at the dataset start: %s", statement)
	}
	if strings.Contains(normalized, "l.created_at>=?") {
		t.Fatalf("risk repair must not consume the shared frozen window start: %s", statement)
	}

	legacy, err := legacyBoundedRepairStatement(riskFingerprintUpsert(), riskDescriptor.repairWindowSQL(rollupRepairWindowDays))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ToLower(legacy), "l.created_at>=greatest("+strings.ToLower(riskFingerprintWindowSQL)+", coalesce(?, '1970-01-01')) and l.id<=?") {
		t.Fatalf("legacy risk repair must inline its floored retention window: %s", legacy)
	}

	// The day layer outlives the call log, so it must not be cleared over the
	// shared window either: a rebuild can only replay what the log still holds.
	// bp_call_api_metric_day carries the 7-day trend and was the projection
	// whose history a shared-window repair destroyed on 2026-10-14, so it must
	// carry the same bound as its sibling day layers.
	for _, index := range []int{projectionAPIDay, projectionMetricDay, projectionUserMetricDay} {
		descriptor := repairProjectionDescriptors[index]
		if !descriptor.hasOwnRepairWindow() {
			t.Fatalf("%s must not share the multi-day repair window", descriptor.name)
		}
		if got := descriptor.repairWindowSQL(rollupRepairWindowDays); got != dayLayerRebuildWindowSQL {
			t.Fatalf("%s repair window = %q, want %q", descriptor.name, got, dayLayerRebuildWindowSQL)
		}
	}

	// Every remaining projection keeps the historical shared-window behaviour.
	for _, descriptor := range repairProjectionDescriptors {
		if descriptor.hasOwnRepairWindow() {
			continue
		}
		if got := descriptor.repairWindowSQL(7); got != "date_sub(current_date(), interval 7 day)" {
			t.Fatalf("%s repair window = %q, want the shared 7-day bound", descriptor.name, got)
		}
	}
}

// A resumable repair step executes its statement with exactly the arguments the
// SQL declares. Binding one argument too many is rejected by the driver before a
// single row is touched, so the persisted repair used to fail on its first CLEAR
// chunk and then retry forever while every projection stayed untouched.
func TestResumableRepairChunksBindExactlyDeclaredPlaceholders(t *testing.T) {
	windowStart := time.Date(2026, 10, 13, 21, 0, 0, 0, time.UTC)
	floor := sqlNullTime(time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC))
	for _, descriptor := range repairProjectionDescriptors {
		if descriptor.isDailyProjection() {
			// Daily projections are rebuilt from their minute projections in
			// FINALIZE and never pass through the log chunk helpers.
			continue
		}
		query, args := repairClearChunk(descriptor, windowStart, floor, repairClearBatchSize)
		if got, want := strings.Count(query, "?"), len(args); got != want {
			t.Fatalf("%s clear chunk declares %d placeholders but binds %d: %s", descriptor.name, got, want, query)
		}
		statement, args, err := repairRollupChunk(descriptor, 10, 20, windowStart, floor)
		if err != nil {
			t.Fatalf("%s rebuild chunk: %v", descriptor.name, err)
		}
		if got, want := strings.Count(statement, "?"), len(args); got != want {
			t.Fatalf("%s rebuild chunk declares %d placeholders but binds %d: %s", descriptor.name, got, want, statement)
		}
	}
}

// The nightly cleanup truncates the call log while the day layers keep their
// history, so every repair bound must stop at the current dataset floor. On
// 2026-10-14 a spurious repair cleared the day layers over the shared 31-day
// window and replayed a one-day log: the accumulated trend history was deleted
// beyond recovery. These tests pin the floor into every repair shape.
func TestRepairBoundsAreFlooredAtTheCurrentDataset(t *testing.T) {
	floor := sqlNullTime(time.Date(2026, 10, 14, 1, 30, 0, 0, time.Local))
	windowStart := time.Date(2026, 9, 13, 0, 0, 0, 0, time.Local)

	// Shared-window projections bind the frozen (already floored) start. The
	// daily projections never flow through these chunk helpers: they are
	// rebuilt from their minute projections in FINALIZE (see
	// TestDailyProjectionsAreRebuiltFromMinuteLayer).
	sharedDescriptor := repairProjectionDescriptors[projectionMetricMinute]
	clearQuery, clearArgs := repairClearChunk(sharedDescriptor, windowStart, floor, repairClearBatchSize)
	if got, want := strings.Count(clearQuery, "?"), len(clearArgs); got != want {
		t.Fatalf("shared clear chunk declares %d placeholders but binds %d: %s", got, want, clearQuery)
	}
	if len(clearArgs) != 1 || clearArgs[0] != any(windowStart) {
		t.Fatalf("shared clear chunk binds the frozen window start, got %v", clearArgs)
	}
	statement, rebuildArgs, err := repairRollupChunk(sharedDescriptor, 10, 20, windowStart, floor)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Count(statement, "?"), len(rebuildArgs); got != want {
		t.Fatalf("shared rebuild declares %d placeholders but binds %d: %s", got, want, statement)
	}
	if len(rebuildArgs) != 3 {
		t.Fatalf("shared rebuild binds cursor, chunk upper and the frozen window start, got %v", rebuildArgs)
	}

	legacy, legacyErr := legacyBoundedRepairStatement(overallMinuteUpsert, "date_sub(current_date(), interval 1 day)")
	if legacyErr != nil {
		t.Fatal(legacyErr)
	}
	normalized := strings.ToLower(legacy)
	if !strings.Contains(normalized, "l.created_at>=greatest(date_sub(current_date(), interval 1 day), coalesce(?, '1970-01-01')) and l.id<=?") {
		t.Fatalf("legacy repair must floor its inlined window: %s", legacy)
	}
}

// floorBoundedWindowStart freezes a repair at the later of the requested
// window and the exact replay floor, rounded up to the next whole minute so a
// partially replayable bucket is never cleared.
func TestFloorBoundedWindowStart(t *testing.T) {
	windowStart := time.Date(2026, 9, 13, 0, 0, 0, 0, time.Local)
	floor := sqlNullTime(time.Date(2026, 10, 14, 1, 30, 0, 0, time.Local))
	if got := floorBoundedWindowStart(windowStart, floor); !got.Equal(time.Date(2026, 10, 14, 1, 30, 0, 0, time.Local)) {
		t.Fatalf("floored window start = %v, want the exact replay floor", got)
	}
	partial := sqlNullTime(time.Date(2026, 10, 14, 1, 30, 37, 0, time.Local))
	if got := floorBoundedWindowStart(windowStart, partial); !got.Equal(time.Date(2026, 10, 14, 1, 31, 0, 0, time.Local)) {
		t.Fatalf("floored window start = %v, want the next full minute", got)
	}
	if got := floorBoundedWindowStart(windowStart, sql.NullTime{}); !got.Equal(windowStart) {
		t.Fatalf("a missing floor must leave the window start untouched, got %v", got)
	}
	oldFloor := sqlNullTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.Local))
	if got := floorBoundedWindowStart(windowStart, oldFloor); !got.Equal(windowStart) {
		t.Fatalf("a floor older than the window must not move the start, got %v", got)
	}
}

func TestRollupRepairMaskRebuildsAllCommonProjectionsWhenTotalsDisagree(t *testing.T) {
	now := time.Date(2026, time.September, 3, 12, 34, 56, 0, time.Local)
	coverage := rollupRepairCoverage{
		sourceLatest: sqlNullTime(now),
	}
	for _, index := range []int{
		projectionMetricMinute,
		projectionAPIMinute,
		projectionUserMinute,
		projectionRegionIPHour,
		projectionRegionHour,
	} {
		coverage.metrics[index] = projectionMetric{
			callCount: 100,
			latest:    sqlNullTime(now),
			available: true,
		}
	}
	// A duplicated/anomalous projection must not become the reference value.
	// Keeping it would leave the other four projections permanently behind.
	coverage.metrics[projectionRegionHour] = projectionMetric{
		callCount: 140,
		latest:    sqlNullTime(now),
		available: true,
	}

	want := uint16(0)
	for _, index := range []int{
		projectionMetricMinute,
		projectionAPIMinute,
		projectionUserMinute,
		projectionRegionIPHour,
		projectionRegionHour,
	} {
		want |= projectionRepairBit(index)
	}
	if got := coverage.repairMask(); got != want {
		t.Fatalf("repairMask() = %#x, want %#x", got, want)
	}
}

func TestRollupRepairMaskRepairsEmptyCommonWindow(t *testing.T) {
	now := time.Date(2026, time.September, 3, 12, 0, 0, 0, time.Local)
	coverage := rollupRepairCoverage{sourceLatest: sqlNullTime(now)}
	for _, index := range []int{
		projectionMetricMinute,
		projectionAPIMinute,
		projectionUserMinute,
		projectionRegionIPHour,
		projectionRegionHour,
	} {
		coverage.metrics[index] = projectionMetric{available: true}
	}

	want := uint16(0)
	for _, index := range []int{
		projectionMetricMinute,
		projectionAPIMinute,
		projectionUserMinute,
		projectionRegionIPHour,
		projectionRegionHour,
	} {
		want |= projectionRepairBit(index)
	}
	if got := coverage.repairMask(); got != want {
		t.Fatalf("repairMask() = %#x, want %#x", got, want)
	}
}

func TestRollupRepairMaskHonorsSourceEligibility(t *testing.T) {
	now := time.Date(2026, time.September, 3, 12, 0, 0, 0, time.Local)
	old := now.Add(-2 * time.Hour)
	coverage := rollupRepairCoverage{
		sourceLatest: sqlNullTime(now),
	}
	for _, index := range []int{
		projectionMetricMinute,
		projectionAPIMinute,
		projectionUserMinute,
		projectionRegionIPHour,
		projectionRegionHour,
	} {
		coverage.metrics[index] = projectionMetric{
			callCount: 1,
			latest:    sqlNullTime(now),
			available: true,
		}
	}
	coverage.metrics[projectionFailureIPHour] = projectionMetric{latest: sqlNullTime(old), available: true}
	coverage.metrics[projectionFailureUserHour] = projectionMetric{latest: sqlNullTime(old), available: true}
	coverage.metrics[projectionRiskFingerprint] = projectionMetric{latest: sqlNullTime(old), available: true}

	// There is no eligible failure/risk source in this coverage. The stale
	// destination rows must not trigger needless destructive rebuilds.
	if got := coverage.repairMask(); got != 0 {
		t.Fatalf("repairMask() = %#x, want no repair for absent failure/risk sources", got)
	}
}

func TestRollupRepairMaskDoesNotRequireOptionalDailyProjection(t *testing.T) {
	now := time.Date(2026, time.September, 3, 12, 0, 0, 0, time.Local)
	coverage := rollupRepairCoverage{sourceLatest: sqlNullTime(now)}
	for _, index := range []int{
		projectionMetricMinute,
		projectionAPIMinute,
		projectionUserMinute,
		projectionRegionIPHour,
		projectionRegionHour,
	} {
		coverage.metrics[index] = projectionMetric{
			callCount: 1,
			latest:    sqlNullTime(now),
			available: true,
		}
	}

	if got := coverage.repairMask(); got&projectionRepairBit(projectionAPIDay) != 0 {
		t.Fatalf("repairMask() unexpectedly requires absent optional daily projection: %#x", got)
	}
}

func TestRollupRepairMaskDetectsStaleCommonWatermarks(t *testing.T) {
	now := time.Date(2026, time.September, 3, 12, 0, 0, 0, time.Local)
	coverage := rollupRepairCoverage{sourceLatest: sqlNullTime(now)}
	for _, index := range []int{
		projectionMetricMinute,
		projectionAPIMinute,
		projectionUserMinute,
		projectionRegionIPHour,
		projectionRegionHour,
	} {
		coverage.metrics[index] = projectionMetric{
			callCount: 1,
			latest:    sqlNullTime(now.Add(-time.Hour)),
			available: true,
		}
	}

	want := uint16(0)
	for _, index := range []int{
		projectionMetricMinute,
		projectionAPIMinute,
		projectionUserMinute,
		projectionRegionIPHour,
		projectionRegionHour,
	} {
		want |= projectionRepairBit(index)
	}
	if got := coverage.repairMask(); got != want {
		t.Fatalf("repairMask() = %#x, want %#x", got, want)
	}
}

func TestRollupRepairMaskDetectsUniformlyMissingRecentSourceRows(t *testing.T) {
	now := time.Date(2026, time.September, 3, 12, 0, 0, 0, time.Local)
	coverage := rollupRepairCoverage{
		sourceLatest:         sqlNullTime(now),
		failureWithIPLatest:  sqlNullTime(now),
		failureLatest:        sqlNullTime(now),
		riskLatest:           sqlNullTime(now),
		sourceCoverageReady:  true,
		sourceCallCount:      7,
		sourceFailureCount:   1,
		sourceFailureIPCount: 1,
		sourceRiskCallCount:  7,
	}
	for _, index := range []int{
		projectionMetricMinute,
		projectionAPIMinute,
		projectionUserMinute,
		projectionRegionIPHour,
		projectionRegionHour,
	} {
		coverage.metrics[index] = projectionMetric{
			callCount:    2,
			failureCount: 0,
			latest:       sqlNullTime(now),
			available:    true,
		}
	}
	coverage.metrics[projectionAPIDay] = projectionMetric{
		callCount:    2,
		failureCount: 0,
		latest:       sqlNullTime(now),
		available:    true,
	}
	coverage.metrics[projectionFailureIPHour] = projectionMetric{
		failureCount: 0,
		latest:       sqlNullTime(now),
		available:    true,
	}
	coverage.metrics[projectionFailureUserHour] = projectionMetric{
		failureCount: 0,
		latest:       sqlNullTime(now),
		available:    true,
	}
	coverage.metrics[projectionRiskFingerprint] = projectionMetric{
		callCount: 2,
		latest:    sqlNullTime(now),
		available: true,
	}

	mask := coverage.repairMask()
	for _, index := range []int{
		projectionMetricMinute,
		projectionAPIMinute,
		projectionAPIDay,
		projectionUserMinute,
		projectionRegionIPHour,
		projectionRegionHour,
		projectionFailureIPHour,
		projectionFailureUserHour,
	} {
		if mask&projectionRepairBit(index) == 0 {
			t.Fatalf("repairMask() = %#x, missing uniformly omitted projection %d", mask, index)
		}
	}
	// The risk projection is explicitly exempt from the raw-count comparison.
	// Its rows are floored to five-minute buckets and retained for a fixed
	// window, so the newest table bucket legitimately covers source rows from
	// before the window boundary and the two counts can never be made equal.
	// Flagging it here would rebuild a 24-hour table over a month of source
	// rows on every probe. Its watermark is current, so it must stay clear.
	if mask&projectionRepairBit(projectionRiskFingerprint) != 0 {
		t.Fatalf("repairMask() = %#x, risk projection must not be repaired on a count mismatch alone", mask)
	}
}

// A stalled or emptied risk projection is still detected: an empty table never
// carries a valid watermark, and a stalled one falls behind the source bucket.
func TestRollupRepairMaskDetectsStalledRiskProjection(t *testing.T) {
	now := time.Date(2026, time.September, 3, 12, 0, 0, 0, time.Local)
	for _, tc := range []struct {
		name    string
		risk    projectionMetric
		wantBit bool
	}{
		{
			name:    "empty table",
			risk:    projectionMetric{},
			wantBit: true,
		},
		{
			name:    "stalled one bucket behind",
			risk:    projectionMetric{latest: sqlNullTime(now.Add(-5 * time.Minute)), available: true},
			wantBit: true,
		},
		{
			name:    "current",
			risk:    projectionMetric{latest: sqlNullTime(now), available: true},
			wantBit: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			coverage := rollupRepairCoverage{
				sourceLatest: sqlNullTime(now),
				riskLatest:   sqlNullTime(now),
			}
			coverage.metrics[projectionRiskFingerprint] = tc.risk
			got := coverage.repairMask()&projectionRepairBit(projectionRiskFingerprint) != 0
			if got != tc.wantBit {
				t.Fatalf("risk repair bit = %v, want %v for %+v", got, tc.wantBit, tc.risk)
			}
		})
	}
}

// The day layer is optional, so an absent table must never be marked for
// repair; when it is present its totals must agree with the common reference.
func TestRollupRepairMaskHandlesOptionalDayLayer(t *testing.T) {
	now := time.Date(2026, time.September, 3, 12, 0, 0, 0, time.Local)
	base := func() rollupRepairCoverage {
		coverage := rollupRepairCoverage{
			sourceLatest: sqlNullTime(now),
		}
		for _, index := range commonProjectionIndexes {
			coverage.metrics[index] = projectionMetric{callCount: 5, latest: sqlNullTime(now), available: true}
		}
		return coverage
	}

	absent := base()
	if absent.repairMask() != 0 {
		t.Fatalf("repairMask() = %#x, an absent optional day layer must not request repair", absent.repairMask())
	}

	current := base()
	for _, index := range []int{projectionMetricDay, projectionUserMetricDay} {
		current.metrics[index] = projectionMetric{callCount: 5, latest: sqlNullTime(now), available: true}
	}
	if current.repairMask() != 0 {
		t.Fatalf("repairMask() = %#x, a caught-up day layer must not request repair", current.repairMask())
	}

	stale := base()
	for _, index := range []int{projectionMetricDay, projectionUserMetricDay} {
		stale.metrics[index] = projectionMetric{callCount: 5, latest: sqlNullTime(now.Add(-24 * time.Hour)), available: true}
	}
	want := projectionRepairBit(projectionMetricDay) | projectionRepairBit(projectionUserMetricDay)
	if got := stale.repairMask(); got != want {
		t.Fatalf("repairMask() = %#x, want %#x for a day layer one day behind", got, want)
	}

	mismatched := base()
	for _, index := range []int{projectionMetricDay, projectionUserMetricDay} {
		mismatched.metrics[index] = projectionMetric{callCount: 4, latest: sqlNullTime(now), available: true}
	}
	if got := mismatched.repairMask(); got != want {
		t.Fatalf("repairMask() = %#x, want %#x when the day layer disagrees with the common totals", got, want)
	}
}

func TestPersistedRepairStatusAndWindowNormalization(t *testing.T) {
	for _, status := range []string{repairStatusPending, repairStatusRunning} {
		if !(rollupState{RepairStatus: status}).needsPersistedRepair() {
			t.Fatalf("status %q should request repair", status)
		}
	}
	for _, status := range []string{repairStatusNone, repairStatusComplete, "unexpected"} {
		if (rollupState{RepairStatus: status}).needsPersistedRepair() {
			t.Fatalf("status %q should not request repair", status)
		}
	}
	for _, tc := range []struct {
		input int
		want  int
	}{
		{input: -1, want: rollupRepairWindowDays},
		{input: 0, want: rollupRepairWindowDays},
		{input: 31, want: 31},
		{input: maxRepairWindowDays + 1, want: maxRepairWindowDays},
	} {
		if got := normalizeRepairWindowDays(tc.input); got != tc.want {
			t.Fatalf("normalizeRepairWindowDays(%d) = %d, want %d", tc.input, got, tc.want)
		}
	}
}

func TestRollupStateRequiresBoundedRecoveryWhenCheckpointIsZeroButRollupExists(t *testing.T) {
	cases := []struct {
		name  string
		state rollupState
		want  bool
	}{
		{
			name: "prepopulated projection",
			state: rollupState{
				CheckpointID:      0,
				NewestLogID:       900,
				HasEligibleSource: true,
				HasAnyRollup:      true,
			},
			want: true,
		},
		{
			name: "no projection",
			state: rollupState{
				CheckpointID:      0,
				NewestLogID:       900,
				HasEligibleSource: true,
			},
			want: false,
		},
		{
			name: "no eligible source",
			state: rollupState{
				CheckpointID: 0,
				NewestLogID:  900,
				HasAnyRollup: true,
			},
			want: false,
		},
		{
			name: "nonzero checkpoint",
			state: rollupState{
				CheckpointID:      1,
				NewestLogID:       900,
				HasEligibleSource: true,
				HasAnyRollup:      true,
			},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.state.needsCheckpointZeroRepair(); got != tc.want {
				t.Fatalf("needsCheckpointZeroRepair() = %v, want %v for %+v", got, tc.want, tc.state)
			}
		})
	}
}

func TestAllProjectionRepairMaskIncludesOptionalDailyProjection(t *testing.T) {
	want := uint16(0)
	for index := range repairProjectionDescriptors {
		want |= projectionRepairBit(index)
	}
	if got := allProjectionRepairMask(); got != want {
		t.Fatalf("allProjectionRepairMask() = %#x, want %#x", got, want)
	}
}

func sqlNullTime(value time.Time) sql.NullTime {
	return sql.NullTime{Time: value, Valid: true}
}
