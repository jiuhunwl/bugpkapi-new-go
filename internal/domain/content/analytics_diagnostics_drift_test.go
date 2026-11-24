package content

import (
	"testing"

	"bugpk-api/internal/domain/analytics"
)

// TestAnalyticsDiagnosticStallWindowsMatchHomepageReader pins the shared stall
// windows to the homepage truth contract.
//
// internal/domain/content deliberately does not import the analytics domain, so
// the read-only diagnostics report carries its own copies of these durations.
// Without this guard the two can drift: the homepage would keep degrading a
// stalled rollup while the operator-facing report calls the same pipeline
// healthy (or the reverse), and the difference would only surface during an
// incident — exactly when it is most expensive.
func TestAnalyticsDiagnosticStallWindowsMatchHomepageReader(t *testing.T) {
	if analytics.DiagnosticWorkerStallAfter != homeAnalyticsWorkerStallAfter {
		t.Fatalf("diagnostic worker stall window = %s, homepage reader uses %s",
			analytics.DiagnosticWorkerStallAfter, homeAnalyticsWorkerStallAfter)
	}
}
