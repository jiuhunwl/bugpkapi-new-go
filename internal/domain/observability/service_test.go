package observability

import (
	"strings"
	"testing"
	"time"

	"bugpk-api/internal/domain/rbac"
)

func TestParseCleanupBeforeRejectsInvalidAndFutureValues(t *testing.T) {
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.FixedZone("expected business +08", 8*60*60))
	for _, value := range []string{"", "not-a-date", "2026-07-17 12:00:00", "2099-01-01T00:00:00Z"} {
		if _, err := parseCleanupBefore(value, now); err == nil {
			t.Fatalf("parseCleanupBefore(%q) accepted unsafe value", value)
		}
	}
}

func TestParseCleanupBeforeNormalizesSupportedFormats(t *testing.T) {
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.FixedZone("expected business +08", 8*60*60))
	if got, err := parseCleanupBefore("2026-07-16 11:22:33", now); err != nil || got != "2026-07-16 11:22:33" {
		t.Fatalf("local cleanup time = %q, %v", got, err)
	}
	rfcValue := time.Date(2026, 7, 16, 11, 22, 33, 0, time.FixedZone("expected business +08", 8*60*60)).Format(time.RFC3339)
	if got, err := parseCleanupBefore(rfcValue, now); err != nil || got != "2026-07-16 11:22:33" {
		t.Fatalf("RFC3339 cleanup time = %q, %v", got, err)
	}
}

func TestDefaultCleanupCutoffIncludesYesterdayLogs(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 4, 52, 0, time.FixedZone("expected business +08", 8*60*60))
	got := defaultCleanupCutoff(now)
	want := "2026-08-16 23:59:59"
	if got != want {
		t.Fatalf("defaultCleanupCutoff() = %q, want %q", got, want)
	}
	if _, err := parseCleanupBefore(got, now); err != nil {
		t.Fatalf("default cleanup cutoff is not accepted: %v", err)
	}
}

func TestSensitiveCallLogsRequireAdminRole(t *testing.T) {
	if canViewSensitiveCallLogs(rbac.Principal{Roles: []string{"user", "log-auditor"}}) {
		t.Fatal("non-admin log auditors must not receive unmasked call-log data")
	}
	if !canViewSensitiveCallLogs(rbac.Principal{Roles: []string{"admin"}}) {
		t.Fatal("admin role should retain unmasked call-log access")
	}
}

func TestDashboardAggregationWindowRemainsBounded(t *testing.T) {
	if dashboardAggregationDays < 1 || dashboardAggregationDays > 90 {
		t.Fatalf("dashboard aggregation window = %d days, want 1..90", dashboardAggregationDays)
	}
}

func TestUserCallLogDTOOnlyContainsPublicBusinessData(t *testing.T) {
	item := toUserCallLogItem(CallLogItem{
		BillingSnapshot: `{"price":"1.0000"}`,
		ClientIP:        "203.0.113.42",
		RequestParams:   `{"password":"secret","normal":"ok","internalRoute":"http://127.0.0.1/admin"}`,
		UpstreamURL:     "https://private-upstream.example/internal?token=secret",
	})
	if item.RequestParams == `{"password":"secret","normal":"ok","internalRoute":"http://127.0.0.1/admin"}` || strings.Contains(item.RequestParams, "internalRoute") {
		t.Fatalf("user log did not mask sensitive fields: %+v", item)
	}
}

func TestUserCallLogPagePropagatesNextCursor(t *testing.T) {
	result := PageResult[CallLogItem]{
		Items:      []CallLogItem{{ID: "42", CreateTime: "2026-08-23 10:00:00", RequestMethod: "GET"}},
		Total:      100,
		NextCursor: encodeCallLogCursor(callLogCursor{CreatedAt: "2026-08-23 10:00:00", ID: 42}),
	}
	userPage := userCallLogPage(result)
	if len(userPage.Items) != 1 || userPage.Total != 100 {
		t.Fatalf("user page lost items/total: %+v", userPage)
	}
	if userPage.NextCursor != result.NextCursor {
		t.Fatalf("user page lost nextCursor %q, got %q", result.NextCursor, userPage.NextCursor)
	}
}
