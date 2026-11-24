package analyticswin

import (
	"testing"
	"time"
)

func windowTestAnchor() time.Time {
	return time.Date(2026, time.October, 13, 12, 0, 0, 0, time.UTC)
}

func TestLayerForPicksCoarsestCoveringLayer(t *testing.T) {
	anchor := windowTestAnchor()
	cases := []struct {
		name string
		span time.Duration
		want Grain
	}{
		{"one minute window uses minute layer", time.Minute, GrainMinute},
		{"one hour window uses minute layer", time.Hour, GrainMinute},
		{"24h dashboard uses minute layer", 24 * time.Hour, GrainMinute},
		{"exactly 36h still uses minute layer", MinuteSpan, GrainMinute},
		{"one tick past 36h promotes to hour layer", MinuteSpan + time.Nanosecond, GrainHour},
		{"7 day dashboard uses hour layer", 7 * 24 * time.Hour, GrainHour},
		{"30 day dashboard uses hour layer", 30 * 24 * time.Hour, GrainHour},
		{"exactly 45d still uses hour layer", HourSpan, GrainHour},
		{"one tick past 45d promotes to day layer", HourSpan + time.Nanosecond, GrainDay},
		{"one year history uses day layer", 365 * 24 * time.Hour, GrainDay},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := LayerFor(anchor, anchor.Add(tc.span)); got != tc.want {
				t.Fatalf("LayerFor(span=%s)=%s, want %s", tc.span, got, tc.want)
			}
		})
	}
}

func TestLayerForEmptyOrReversedWindowFallsBackToMinute(t *testing.T) {
	anchor := windowTestAnchor()
	for _, tc := range []struct {
		name     string
		from, to time.Time
	}{
		{"zero window", anchor, anchor},
		{"reversed window", anchor, anchor.Add(-48 * time.Hour)},
		{"zero value window", time.Time{}, time.Time{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := LayerFor(tc.from, tc.to); got != GrainMinute {
				t.Fatalf("LayerFor(%v,%v)=%s, want minute", tc.from, tc.to, got)
			}
		})
	}
}

func TestGrainSuffixAndTableName(t *testing.T) {
	cases := []struct {
		grain  Grain
		suffix string
		table  string
	}{
		{GrainMinute, "minute", "bp_call_metric_minute"},
		{GrainHour, "hour", "bp_call_metric_hour"},
		{GrainDay, "day", "bp_call_metric_day"},
	}
	for _, tc := range cases {
		if got := tc.grain.String(); got != tc.suffix {
			t.Fatalf("Grain(%d).String()=%q, want %q", tc.grain, got, tc.suffix)
		}
		if got := TableName("bp_call_metric_", tc.grain); got != tc.table {
			t.Fatalf("TableName(prefix, %s)=%q, want %q", tc.grain, got, tc.table)
		}
	}
}

// The layer boundaries are a contract with the retention/latency plan; changing
// them silently would let a wide window fall back onto an expensive layer.
func TestLayerBoundariesMatchThePlan(t *testing.T) {
	if MinuteSpan != 36*time.Hour {
		t.Fatalf("minute layer span changed to %s, want 36h", MinuteSpan)
	}
	if HourSpan != 45*24*time.Hour {
		t.Fatalf("hour layer span changed to %s, want 45d", HourSpan)
	}
	if !(MinuteSpan < HourSpan) {
		t.Fatal("minute layer must be narrower than the hour layer")
	}
}
