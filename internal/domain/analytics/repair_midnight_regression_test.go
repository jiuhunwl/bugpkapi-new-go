package analytics

import (
	"strings"
	"testing"
)

// The daily projections outlive the raw log, so a repair must never replay
// them from a source the retention sweep has truncated mid-day. They are
// recomputed from their minute projections over the minute layer's retention
// window instead, and the window floor is read once inside the FINALIZE
// transaction (bound as a parameter) rather than per chunk, so a step that
// crosses midnight clears and rebuilds the same days.
func TestDailyProjectionsAreRebuiltFromMinuteLayer(t *testing.T) {
	if dayLayerRebuildWindowSQL != "date_sub(current_date(), interval 3 day)" {
		t.Fatalf("daily rebuild window = %q", dayLayerRebuildWindowSQL)
	}
	sources := map[int]string{
		projectionAPIDay:        "from bp_call_api_metric_minute",
		projectionMetricDay:     "from bp_call_metric_minute",
		projectionUserMetricDay: "from bp_call_user_metric_minute",
	}
	for _, index := range []int{projectionAPIDay, projectionMetricDay, projectionUserMetricDay} {
		statement, ok := dailyProjectionRebuildStatements[index]
		if !ok {
			t.Fatalf("%s has no minute-sourced rebuild statement", repairProjectionDescriptors[index].name)
		}
		normalized := strings.ToLower(statement)
		if !strings.Contains(normalized, sources[index]) {
			t.Fatalf("%s rebuild does not read its minute projection: %s", repairProjectionDescriptors[index].name, statement)
		}
		if !strings.Contains(normalized, "where bucket_start>=?") || strings.Contains(normalized, "current_date()") {
			t.Fatalf("%s rebuild must bind its frozen window floor: %s", repairProjectionDescriptors[index].name, statement)
		}
		if !strings.Contains(normalized, "on duplicate key update") || !strings.Contains(normalized, "call_count=values(call_count)") {
			t.Fatalf("%s rebuild must replace existing rows so a retried step is idempotent: %s", repairProjectionDescriptors[index].name, statement)
		}
	}
}
