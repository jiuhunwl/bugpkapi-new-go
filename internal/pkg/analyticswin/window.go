// Package analyticswin centralises the window-to-grain decision shared by every
// statistics reader. Keeping the rule in one place lets business SQL avoid
// hard-coding a rollup table name, so the scanned row count stays bounded as a
// window grows instead of scaling with the traffic volume behind it.
//
// The layer boundaries mirror the retention/latency contract:
//
//	minute  <= 36h   near-real-time curve (finest grain, shortest retention)
//	hour    <= 45d   day-scale dashboards (7/30-day trends, region breakdowns)
//	day     >  45d   long-range rankings and history (cheapest per row)
//
// A reader that spans a boundary (for example "today plus the last 30 days")
// must split the window into per-layer segments and merge them, so the wide
// segment is never served by the expensive minute layer.
package analyticswin

import "time"

// Grain identifies which rollup layer a time window should be served from.
type Grain int

const (
	// GrainMinute is the per-minute layer for short, near-real-time windows.
	GrainMinute Grain = iota
	// GrainHour is the per-hour layer for day-scale dashboard windows.
	GrainHour
	// GrainDay is the per-day layer for long-range rankings and history.
	GrainDay
)

const (
	// MinuteSpan is the widest window still served by the minute layer.
	MinuteSpan = 36 * time.Hour
	// HourSpan is the widest window still served by the hour layer.
	HourSpan = 45 * 24 * time.Hour
)

// LayerFor returns the coarsest layer that still covers the window, so the
// number of rows a reader must scan stays bounded as the span grows. A window
// that is empty or reversed has no meaningful span and falls back to the
// fine-grained layer, which is always safe: any bucket a shorter window can
// address exists there too.
func LayerFor(from, to time.Time) Grain {
	if !to.After(from) {
		return GrainMinute
	}
	switch span := to.Sub(from); {
	case span <= MinuteSpan:
		return GrainMinute
	case span <= HourSpan:
		return GrainHour
	default:
		return GrainDay
	}
}

// String renders the grain as its rollup-table suffix ("minute"/"hour"/"day"),
// which doubles as the stable label used in logs and diagnostics.
func (g Grain) String() string {
	switch g {
	case GrainHour:
		return "hour"
	case GrainDay:
		return "day"
	default:
		return "minute"
	}
}

// TableName builds a rollup table name for the grain, e.g.
// TableName("bp_call_metric_", GrainHour) == "bp_call_metric_hour". Readers use
// this instead of concatenating suffixes by hand so the mapping cannot drift.
func TableName(prefix string, g Grain) string {
	return prefix + g.String()
}
