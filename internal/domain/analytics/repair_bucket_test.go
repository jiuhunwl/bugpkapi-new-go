package analytics

import (
	"database/sql"
	"testing"
	"time"
)

func TestRollupRepairUsesEachProjectionBucketWidth(t *testing.T) {
	zone := time.FixedZone("UTC+08:00", 8*60*60)
	source := time.Date(2026, time.October, 10, 14, 37, 46, 0, zone)
	minute := time.Date(2026, time.October, 10, 14, 37, 0, 0, zone)
	hour := time.Date(2026, time.October, 10, 14, 0, 0, 0, zone)
	coverage := rollupRepairCoverage{sourceLatest: sql.NullTime{Time: source, Valid: true}, sourceCoverageReady: true, sourceCallCount: 1_000_000}
	for _, index := range commonProjectionIndexes {
		bucket := minute
		if index == projectionRegionIPHour || index == projectionRegionHour {
			bucket = hour
		}
		coverage.metrics[index] = projectionMetric{available: true, callCount: 1_000_000, latest: sql.NullTime{Time: bucket, Valid: true}}
	}
	if mask := coverage.repairMask(); mask != 0 {
		t.Fatalf("correct hourly buckets requested destructive rebuild: mask=%#x", mask)
	}
	for _, index := range []int{projectionRegionIPHour, projectionRegionHour} {
		stale := coverage
		stale.metrics[index].latest = sql.NullTime{Time: hour.Add(-time.Hour), Valid: true}
		if mask := stale.repairMask(); mask != projectionRepairBit(index) {
			t.Errorf("real stale hourly projection not isolated: got=%#x want=%#x", mask, projectionRepairBit(index))
		}
	}
}
