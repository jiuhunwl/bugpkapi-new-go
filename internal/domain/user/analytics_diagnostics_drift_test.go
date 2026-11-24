package user

import (
	"testing"

	"bugpk-api/internal/domain/analytics"
)

// TestAnalyticsDiagnosticStallWindowsMatchAdminReader pins the shared stall
// windows to the admin dashboard's repair-state reader.
//
// internal/domain/analytics cannot import this package (it would invert the
// dependency direction), so the read-only diagnostics report carries its own
// copy of the repair stall window. Without this guard the two can drift: the
// console would keep suppressing its projection-backed sections while the
// operator-facing report calls the repair healthy, and the difference would only
// surface during an incident.
func TestAnalyticsDiagnosticStallWindowsMatchAdminReader(t *testing.T) {
	if analytics.DiagnosticRepairStallAfter != adminAnalyticsRepairStallAfter {
		t.Fatalf("diagnostic repair stall window = %s, admin reader uses %s",
			analytics.DiagnosticRepairStallAfter, adminAnalyticsRepairStallAfter)
	}
}
