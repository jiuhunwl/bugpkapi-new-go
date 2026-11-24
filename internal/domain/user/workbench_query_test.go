package user

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func TestUserWorkbenchStatsFansOutIndependentQueries(t *testing.T) {
	source, err := os.ReadFile("service.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	start := strings.Index(text, "func (s *Service) UserWorkbenchStats")
	end := strings.Index(text, "var allAdminWorkbenchSections")
	if start < 0 || end <= start {
		t.Fatal("could not isolate UserWorkbenchStats")
	}
	function := text[start:end]
	if !strings.Contains(function, "dbx.RunParallel(") {
		t.Fatalf("UserWorkbenchStats must fan out independent repository queries: %s", function)
	}
}

func TestAdminHotAPIsQueryMatchesTodayFrontendContract(t *testing.T) {
	query := strings.ToLower(adminHotAPIsQuery())
	for _, fragment := range []string{
		"inner join bp_call_api_metric_minute m",
		"m.bucket_start>=current_date()",
		"inner join bp_user u on u.id=m.user_id",
		"sum(m.call_count) today_calls",
		"coalesce(sum(m.success_count),0) success_calls",
		"coalesce(sum(m.failure_count),0) failed_calls",
		"coalesce(sum(m.billable_count),0) billable_calls",
		"cast(coalesce(sum(m.charge_amount),0) as char) charge_amount",
		"sum(m.elapsed_sum)/nullif(sum(m.call_count),0)",
		"case when u.status = 0",
	} {
		if !strings.Contains(query, fragment) {
			t.Fatalf("admin hot API query lost %q: %s", fragment, query)
		}
	}
	if strings.Contains(query, "bp_interface_call_log") {
		t.Fatalf("admin hot API query must not scan the raw call log: %s", query)
	}
	if strings.Contains(query, "interval 30 day") {
		t.Fatalf("admin hot API query must use today's window: %s", query)
	}
}

func TestAdminActiveUsersQueryMatchesTodayFrontendContract(t *testing.T) {
	query := strings.ToLower(adminActiveUsersQuery())
	for _, fragment := range []string{
		"inner join bp_call_user_metric_day m on m.user_id=u.id",
		"m.bucket_date=current_date()",
		"sum(m.call_count) today_calls",
		"coalesce(sum(m.success_count),0) success_calls",
		"cast(coalesce(sum(m.charge_amount),0) as char) charge_amount",
		"case when u.status = 0",
	} {
		if !strings.Contains(query, fragment) {
			t.Fatalf("admin active-user query lost %q: %s", fragment, query)
		}
	}
	if strings.Contains(query, "bp_interface_call_log") {
		t.Fatalf("admin active-user query must not scan the raw call log: %s", query)
	}
	if strings.Contains(query, "interval 7 day") {
		t.Fatalf("admin active-user query must use today's window: %s", query)
	}
	// The ranking reads the day layer, which 000100 introduced. The minute
	// fallback must stay selectable so a database that has not applied that
	// migration keeps a working ranking instead of raising an error.
	fallback := strings.ToLower(adminActiveUsersMinuteQuery())
	if !strings.Contains(fallback, "inner join bp_call_user_metric_minute m on m.user_id=u.id") {
		t.Fatalf("admin active-user minute fallback lost its projection: %s", fallback)
	}
	if !strings.Contains(fallback, "m.bucket_start>=current_date()") {
		t.Fatalf("admin active-user minute fallback must cover the same day: %s", fallback)
	}
}

// The 7-day trend must read the day layer first and widen only one step when a
// table is genuinely absent: day -> all-user minute -> user minute -> raw log.
func TestAdminCallTrendPrefersTheDayProjection(t *testing.T) {
	for name, query := range map[string]string{
		"all-user day":    adminCallTrendAllDayQuery(),
		"per-user day":    adminCallTrendDayQuery(),
		"all-user minute": adminCallTrendAllQuery(),
		"per-user minute": adminCallTrendQuery(),
	} {
		normalized := strings.ToLower(query)
		if strings.Contains(normalized, "bp_interface_call_log") {
			t.Fatalf("%s trend query must not scan raw logs: %s", name, query)
		}
		if !strings.Contains(normalized, "interval 6 day") {
			t.Fatalf("%s trend query must cover the same seven-day window: %s", name, query)
		}
		if !strings.Contains(normalized, "cast(coalesce(sum(m.charge_amount),0) as char)") {
			t.Fatalf("%s trend query lost the money contract: %s", name, query)
		}
	}
	if !strings.Contains(strings.ToLower(adminCallTrendAllDayQuery()), "from bp_call_metric_day m") {
		t.Fatal("all-user trend must read the one-row-per-day projection")
	}
	if !strings.Contains(strings.ToLower(adminCallTrendDayQuery()), "inner join bp_user u on u.id=m.user_id") {
		t.Fatal("per-user trend must keep the read-time account-status join")
	}
	if !strings.Contains(strings.ToLower(adminCallTrendAllDayQuery()), "date_format(m.bucket_date,'%m-%d')") {
		t.Fatal("all-user day trend must keep the MM-DD label contract")
	}
}

func TestAdminAccessLocationQueryAggregatesRegionsWithoutExposingIPs(t *testing.T) {
	query := strings.ToLower(adminAccessLocationsQuery())
	for _, fragment := range []string{
		"from bp_call_region_hour r force index(idx_bp_call_region_hour_time)",
		"r.bucket_start >= date_format(date_sub(now(), interval ? day),'%y-%m-%d %h:00:00')",
		"inner join bp_user u on u.id=r.user_id",
		"group by r.country,r.province,r.city,r.isp,r.latitude,r.longitude",
		"coalesce(sum(r.unique_ip_count),0)",
		"case when u.status = 0",
	} {
		if !strings.Contains(query, fragment) {
			t.Fatalf("admin access query lost %q: %s", fragment, query)
		}
	}
	if strings.Contains(query, "bp_interface_call_log") || strings.Contains(query, "client_ip_hash") {
		t.Fatal("admin access query must neither scan nor expose raw client IPs")
	}
	if adminAccessLocationDays != 30 || adminAccessLocationMax < 1000 {
		t.Fatalf("unexpected admin access bounds: days=%d max=%d", adminAccessLocationDays, adminAccessLocationMax)
	}
}

func TestAdminAccessSummaryQueryKeepsCoordinateLessRowsInTotals(t *testing.T) {
	query := strings.ToLower(adminAccessSummaryQuery())
	if strings.Contains(query, "latitude is not null") || strings.Contains(query, "longitude is not null") {
		t.Fatal("summary query must include calls without coordinates")
	}
}

func TestAdminAccessCombinedQueryUsesOneBoundedSnapshot(t *testing.T) {
	query := strings.ToLower(adminAccessQuery())
	for _, fragment := range []string{
		"with filtered as",
		"from bp_call_region_hour r force index(idx_bp_call_region_hour_time)",
		"inner join bp_user u on u.id=r.user_id",
		"location_groups as",
		"totals as",
		"coalesce(sum(unique_ip_count),0) unique_ips",
		"cross join totals",
	} {
		if !strings.Contains(query, fragment) {
			t.Fatalf("combined access query lost %q: %s", fragment, query)
		}
	}
	if strings.Count(query, "from bp_call_region_hour") != 1 {
		t.Fatalf("combined access query must materialize the rollup window once: %s", query)
	}
	if strings.Contains(query, "bp_interface_call_log") || strings.Contains(query, "client_ip_hash") {
		t.Fatal("combined access query must neither scan nor expose raw client IPs")
	}
}

func TestAdminAccessRegionIPFallbackUsesPrivacySafeRollup(t *testing.T) {
	query := strings.ToLower(adminAccessRegionIPQuery())
	for _, fragment := range []string{
		"from bp_call_region_ip_hour r force index(idx_bp_call_region_ip_time)",
		"client_ip_hash",
		"count(distinct case",
		// 窗口下界现在是绑定参数：范围由 Go 侧换算成具体时间，
		// 让四种范围（今天/7天/30天/全部）复用同一执行计划。
		"r.bucket_start >= ?",
		"inner join bp_user u on u.id=r.user_id",
	} {
		if !strings.Contains(query, fragment) {
			t.Fatalf("region-IP fallback query lost %q: %s", fragment, query)
		}
	}
	if strings.Contains(query, "interval ? day") {
		t.Fatal("access windows must bind an explicit start time, not a day interval")
	}
	if strings.Contains(query, "bp_interface_call_log") {
		t.Fatalf("region-IP fallback must not scan raw call logs: %s", query)
	}
}

// TestAdminAccessRangeWindowStart 锁定范围到下界的换算规则。
//
// 边界必须对齐到自然日 00:00：rollup 的桶是整点，
// 若用 now() 作为「今天」的下界，10:30 发起的查询会漏掉 10:00 那个桶。
func TestAdminAccessRangeWindowStart(t *testing.T) {
	loc := time.FixedZone("CST", 8*3600)
	now := time.Date(2026, 9, 24, 10, 30, 45, 0, loc)

	cases := []struct {
		rng      AdminAccessRange
		expected time.Time
		days     int
	}{
		{AdminAccessRangeToday, time.Date(2026, 9, 24, 0, 0, 0, 0, loc), 1},
		{AdminAccessRange7d, time.Date(2026, 9, 18, 0, 0, 0, 0, loc), 7},
		{AdminAccessRange30d, time.Date(2026, 8, 26, 0, 0, 0, 0, loc), 30},
		// 「全部」被钳制到最近 90 天：2026-09-24 往前 89 天 = 2026-06-27。
		// 窗口天数必须与下界一致，否则前端会把 90 天的数据标成「全部」。
		{AdminAccessRangeAll, time.Date(2026, 6, 27, 0, 0, 0, 0, loc), 90},
	}
	for _, tc := range cases {
		if got := adminAccessWindowStart(tc.rng, now); !got.Equal(tc.expected) {
			t.Fatalf("range %s: got %s, want %s", tc.rng, got, tc.expected)
		}
		if got := adminAccessRangeWindowDays(tc.rng); got != tc.days {
			t.Fatalf("range %s: windowDays got %d, want %d", tc.rng, got, tc.days)
		}
	}
	if got := normalizeAdminAccessRange("nonsense"); got != AdminAccessRange30d {
		t.Fatalf("unknown range must fall back to 30d, got %s", got)
	}
	if got := normalizeAdminAccessRange("ALL"); got != AdminAccessRangeAll {
		t.Fatalf("range parsing must be case-insensitive, got %s", got)
	}
}

func TestAdminOverviewCallsQueryUsesCurrentActiveUserRollup(t *testing.T) {
	query := strings.ToLower(adminOverviewCallsQuery())
	for _, fragment := range []string{
		"from bp_call_user_metric_minute m",
		"inner join bp_user u on u.id=m.user_id",
		"m.bucket_start>=current_date()",
		"sum(m.call_count)",
		"sum(m.success_count)",
		"sum(m.failure_count)",
		"sum(m.billable_count)",
		"case when u.status = 0",
	} {
		if !strings.Contains(query, fragment) {
			t.Fatalf("admin overview call query lost %q: %s", fragment, query)
		}
	}
	if strings.Contains(query, "bp_interface_call_log") {
		t.Fatalf("admin overview call query must not scan raw logs: %s", query)
	}
}

func TestAdminCallTrendQueryUsesCurrentActiveUserRollup(t *testing.T) {
	query := strings.ToLower(adminCallTrendQuery())
	for _, fragment := range []string{
		"from bp_call_user_metric_minute m",
		"inner join bp_user u on u.id=m.user_id",
		"m.bucket_start>=date_sub(current_date(), interval 6 day)",
		"group by date_format(m.bucket_start,'%m-%d')",
		"case when u.status = 0",
	} {
		if !strings.Contains(query, fragment) {
			t.Fatalf("admin call-trend query lost %q: %s", fragment, query)
		}
	}
	if strings.Contains(query, "bp_interface_call_log") {
		t.Fatalf("admin call-trend query must not scan raw logs: %s", query)
	}
}

func TestAdminRevenueTrendUsesPaidTimeCoveringIndex(t *testing.T) {
	query := strings.ToLower(adminRevenueTrendQuery())
	for _, fragment := range []string{
		"force index(idx_bp_payment_order_paid)",
		"status='paid'",
		"paid_at>=date_sub(current_date(), interval 6 day)",
	} {
		if !strings.Contains(query, fragment) {
			t.Fatalf("admin revenue query lost %q: %s", fragment, query)
		}
	}
}

func TestAdminRevenueTrendNoHintUsesTheSameShortDayLabelsAsTheFrontend(t *testing.T) {
	query := strings.ToLower(adminRevenueTrendNoHintQuery())
	if !strings.Contains(query, "select date_format(paid_at,'%m-%d')") {
		t.Fatalf("no-hint revenue fallback must return MM-DD labels: %s", query)
	}
	if strings.Contains(query, "date_format(paid_at,'%y-%m-%d')") {
		t.Fatalf("no-hint revenue fallback must not return full-date labels: %s", query)
	}
}

func TestBuildUserCallTrendsUsesOneDailyAggregateForAllRanges(t *testing.T) {
	now := time.Date(2026, time.January, 15, 10, 0, 0, 0, time.Local)
	daily := map[string]int64{
		now.Format("2006-01-02"):                     2,
		now.AddDate(0, 0, -6).Format("2006-01-02"):   3,
		now.AddDate(0, 0, -7).Format("2006-01-02"):   5,
		now.AddDate(0, 0, -29).Format("2006-01-02"):  7,
		now.AddDate(0, 0, -40).Format("2006-01-02"):  11,
		now.AddDate(0, 0, -330).Format("2006-01-02"): 13,
		now.AddDate(0, 0, -400).Format("2006-01-02"): 17,
	}

	trends := buildUserCallTrends(now, daily)
	if got := trends["sevenDays"].Total; got != 5 {
		t.Fatalf("seven-day total = %d, want 5", got)
	}
	if got := trends["thirtyDays"].Total; got != 17 {
		t.Fatalf("thirty-day total = %d, want 17", got)
	}
	if got := trends["oneYear"].Total; got != 41 {
		t.Fatalf("one-year total = %d, want 41", got)
	}
	if len(trends["sevenDays"].Labels) != 7 || len(trends["thirtyDays"].Labels) != 30 || len(trends["oneYear"].Labels) != 12 {
		t.Fatalf("unexpected trend lengths: seven=%d thirty=%d year=%d", len(trends["sevenDays"].Labels), len(trends["thirtyDays"].Labels), len(trends["oneYear"].Labels))
	}
	if got := trends["sevenDays"].Labels[6]; got != "01-15" {
		t.Fatalf("last daily label = %q, want 01-15", got)
	}
	if got := trends["oneYear"].Labels[11]; got != "2026-01" {
		t.Fatalf("last monthly label = %q, want 2026-01", got)
	}
}

func TestUserDailyCallCountsUsesUserRollupTimeIndex(t *testing.T) {
	query := strings.ToLower(userDailyCallCountsQuery())
	for _, fragment := range []string{
		"from bp_call_user_metric_minute force index(idx_bp_call_user_metric_user_time)",
		"bucket_start>=date_sub(current_date(), interval ? day)",
		"sum(call_count)",
	} {
		if !strings.Contains(query, fragment) {
			t.Fatalf("user daily call query lost %q: %s", fragment, query)
		}
	}
	if strings.Contains(query, "bp_interface_call_log") {
		t.Fatalf("user daily call query must not scan raw logs: %s", query)
	}
}

func TestUserSummaryAndTotalRequestsUseUserRollup(t *testing.T) {
	for name, query := range map[string]string{
		"summary": userCallSummaryQuery(),
		"total":   userTotalRequestsQuery(),
	} {
		query = strings.ToLower(query)
		if !strings.Contains(query, "bp_call_user_metric_minute force index(idx_bp_call_user_metric_user_time)") {
			t.Fatalf("%s query does not use the user rollup index: %s", name, query)
		}
		if strings.Contains(query, "bp_interface_call_log") {
			t.Fatalf("%s query must not scan raw logs: %s", name, query)
		}
	}
}

func TestSummarizeWorkbenchQuotaUsesPrimaryRoleQPSWithoutPackage(t *testing.T) {
	got := summarizeWorkbenchQuota(nil, nil, 25, 80)
	if got.QPSLimit != 25 || got.QPSLimitUnlimited {
		t.Fatalf("primary-role QPS summary = %+v, want limited 25 QPS", got)
	}
	if !got.DailyLimitUnlimited || got.DailyLimit != 0 || got.FromPackage {
		t.Fatalf("base-account daily quota = %+v, want unlimited without package", got)
	}
	if got.RemainingUnlimited || got.RemainingCalls != 80 {
		t.Fatalf("base-account remaining calls = %+v, want points fallback 80", got)
	}
}

func TestSummarizeWorkbenchQuotaPrefersGlobalPackage(t *testing.T) {
	member := &UserWorkbenchMemberPackage{
		DailyLimit:         100,
		QPSLimit:           7,
		RemainingCalls:     60,
		RemainingUnlimited: false,
	}
	interfaces := []UserWorkbenchInterfacePackage{{DailyLimit: 10, QPSLimit: 2, RemainingCalls: 9}}
	got := summarizeWorkbenchQuota(member, interfaces, 25, 80)
	if got.DailyLimit != 100 || got.DailyLimitUnlimited || got.QPSLimit != 25 || got.QPSLimitUnlimited {
		t.Fatalf("global package quota was not preferred: %+v", got)
	}
	if got.RemainingCalls != 60 || got.RemainingUnlimited {
		t.Fatalf("global package remaining quota mismatch: %+v", got)
	}
	if !got.FromPackage {
		t.Fatalf("global package should be marked as preferred: %+v", got)
	}
}

func TestSummarizeWorkbenchQuotaFallsBackWhenGlobalPackageIsDailyExhausted(t *testing.T) {
	member := &UserWorkbenchMemberPackage{
		DailyLimit:     100,
		QPSLimit:       0,
		RemainingCalls: 0,
	}
	got := summarizeWorkbenchQuota(member, nil, 25, 80)
	if got.QPSLimit != 25 || got.QPSLimitUnlimited {
		t.Fatalf("daily-exhausted global package kept stale QPS: %+v", got)
	}
	if !got.DailyLimitUnlimited || got.RemainingCalls != 80 || got.RemainingUnlimited {
		t.Fatalf("daily-exhausted global package did not fall back to base quota: %+v", got)
	}
}

func TestSummarizeWorkbenchQuotaUsesSingleInterfacePackage(t *testing.T) {
	interfaces := []UserWorkbenchInterfacePackage{{
		DailyLimit:         0,
		QPSLimit:           0,
		RemainingUnlimited: true,
	}}
	got := summarizeWorkbenchQuota(nil, interfaces, 25, 80)
	if !got.DailyLimitUnlimited || !got.QPSLimitUnlimited || !got.RemainingUnlimited || !got.FromPackage {
		t.Fatalf("unlimited interface-package quota mismatch: %+v", got)
	}
}

func TestSummarizeWorkbenchQuotaFallsBackWhenInterfacePackageIsDailyExhausted(t *testing.T) {
	interfaces := []UserWorkbenchInterfacePackage{{
		DailyLimit:     10,
		QPSLimit:       0,
		RemainingCalls: 0,
	}}
	got := summarizeWorkbenchQuota(nil, interfaces, 25, 80)
	if got.QPSLimit != 25 || got.QPSLimitUnlimited {
		t.Fatalf("daily-exhausted interface package kept stale QPS: %+v", got)
	}
	if !got.DailyLimitUnlimited || got.RemainingCalls != 80 || got.RemainingUnlimited {
		t.Fatalf("daily-exhausted interface package did not fall back to base quota: %+v", got)
	}
}

func TestSummarizeWorkbenchQuotaDoesNotInventAggregateForMultipleInterfacePackages(t *testing.T) {
	interfaces := []UserWorkbenchInterfacePackage{
		{DailyLimit: 10, QPSLimit: 2, RemainingCalls: 9},
		{DailyLimit: 20, QPSLimit: 4, RemainingCalls: 19},
	}
	got := summarizeWorkbenchQuota(nil, interfaces, 25, 80)
	if got != (workbenchQuotaSummary{}) {
		t.Fatalf("multiple interface packages unexpectedly received one aggregate quota: %+v", got)
	}
}

func TestRemainingDailyCalls(t *testing.T) {
	tests := []struct {
		name      string
		limit     int64
		used      int64
		remaining int64
		unlimited bool
	}{
		{name: "unlimited", limit: 0, used: 999, remaining: 0, unlimited: true},
		{name: "available", limit: 100, used: 40, remaining: 60},
		{name: "exhausted", limit: 100, used: 100, remaining: 0},
		{name: "overused", limit: 100, used: 101, remaining: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			remaining, unlimited := remainingDailyCalls(tt.limit, tt.used)
			if remaining != tt.remaining || unlimited != tt.unlimited {
				t.Fatalf("remainingDailyCalls(%d, %d) = (%d, %v), want (%d, %v)", tt.limit, tt.used, remaining, unlimited, tt.remaining, tt.unlimited)
			}
		})
	}
}

func TestNormalizeRoleQPS(t *testing.T) {
	for _, tt := range []struct {
		input int
		want  int
	}{
		{input: -1, want: 1},
		{input: 0, want: 1},
		{input: 25, want: 25},
		{input: 1001, want: 1001},
		{input: 1_000_001, want: 1_000_000},
	} {
		if got := normalizeRoleQPS(tt.input); got != tt.want {
			t.Fatalf("normalizeRoleQPS(%d) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestUserWorkbenchPackageJSONMatchesFrontendContract(t *testing.T) {
	data, err := json.Marshal(UserWorkbenchStats{
		MemberPackage: &UserWorkbenchMemberPackage{
			PackageID:     "11",
			PackageName:   "Global",
			UserPackageID: "101",
		},
		InterfacePackages: []UserWorkbenchInterfacePackage{{
			APICode:       "phone_lookup",
			InterfaceID:   "21",
			InterfaceName: "Phone lookup",
			PackageID:     "31",
			PackageName:   "Phone package",
			SpecID:        "41",
			SpecName:      "Standard",
			UserPackageID: "201",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatal(err)
	}
	member, ok := payload["memberPackage"].(map[string]any)
	if !ok || member["packageName"] != "Global" || member["userPackageId"] != "101" {
		t.Fatalf("member package JSON contract mismatch: %s", data)
	}
	interfaces, ok := payload["interfacePackages"].([]any)
	if !ok || len(interfaces) != 1 {
		t.Fatalf("interface package JSON contract mismatch: %s", data)
	}
	item, ok := interfaces[0].(map[string]any)
	if !ok || item["apiCode"] != "phone_lookup" || item["interfaceName"] != "Phone lookup" || item["userPackageId"] != "201" {
		t.Fatalf("interface package JSON fields mismatch: %s", data)
	}
	for _, legacyKey := range []string{"endAt", "id", "name", "usedToday"} {
		if _, exists := item[legacyKey]; exists {
			t.Fatalf("legacy interface package field %q leaked: %s", legacyKey, data)
		}
	}
}

func TestAdminActiveUserJSONMatchesFrontendContract(t *testing.T) {
	data, err := json.Marshal(AdminWorkbenchStats{
		ActiveUsers: []AdminActiveUserStats{{
			ChargeAmount: "1.2500",
			ID:           "9",
			RealName:     "Active User",
			SuccessCalls: 8,
			SuccessRate:  "80.00%",
			TodayCalls:   10,
			Username:     "active-user",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatal(err)
	}
	users, ok := payload["activeUsers"].([]any)
	if !ok || len(users) != 1 {
		t.Fatalf("active user JSON contract mismatch: %s", data)
	}
	item, ok := users[0].(map[string]any)
	if !ok || item["todayCalls"] != float64(10) || item["successRate"] != "80.00%" || item["chargeAmount"] != "1.2500" {
		t.Fatalf("active user JSON fields mismatch: %s", data)
	}
	if _, exists := item["callCount"]; exists {
		t.Fatalf("legacy active-user field callCount leaked: %s", data)
	}
}

func TestAdminHotAPIJSONMatchesFrontendContract(t *testing.T) {
	data, err := json.Marshal(AdminWorkbenchStats{
		HotApis: []AdminHotAPIStats{{
			APICode:       "douyin_parse",
			APIStatus:     1,
			AvgElapsedMS:  125,
			BillableCalls: 7,
			ChargeAmount:  "1.2500",
			FailedCalls:   2,
			ID:            "11",
			Name:          "Douyin parser",
			SuccessCalls:  8,
			SuccessRate:   "80.00%",
			TodayCalls:    10,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatal(err)
	}
	items, ok := payload["hotApis"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("hot API JSON contract mismatch: %s", data)
	}
	item, ok := items[0].(map[string]any)
	if !ok || item["todayCalls"] != float64(10) || item["successRate"] != "80.00%" || item["chargeAmount"] != "1.2500" || item["avgElapsedMs"] != float64(125) {
		t.Fatalf("hot API JSON fields mismatch: %s", data)
	}
	if _, exists := item["callCount"]; exists {
		t.Fatalf("legacy hot API field callCount leaked: %s", data)
	}
}

func TestApplyRoleQuotaCapReplacesUnlimitedFallback(t *testing.T) {
	base := workbenchQuotaSummary{
		DailyLimitUnlimited: true,
		QPSLimitUnlimited:   false,
		QPSLimit:            3,
		RemainingCalls:      80,
	}
	got := applyRoleQuotaCap(base, UserRoleQuota{Enabled: 1, Day: 86400, CountMode: "attempt"}, 100)
	if got.DailyLimitUnlimited || got.DailyLimit != 86400 || got.RemainingUnlimited || got.RemainingCalls != 86300 {
		t.Fatalf("role cap not applied to unlimited fallback: %+v", got)
	}
	if got.QPSLimit != 3 {
		t.Fatalf("role cap changed QPS: %+v", got)
	}
}

func TestApplyRoleQuotaCapKeepsLowerPackageDailyLimit(t *testing.T) {
	base := workbenchQuotaSummary{
		DailyLimit:          100,
		DailyLimitUnlimited: false,
		RemainingCalls:      40,
	}
	got := applyRoleQuotaCap(base, UserRoleQuota{Enabled: 1, Day: 86400}, 10)
	if got.DailyLimit != 100 || got.RemainingCalls != 40 || got.RemainingUnlimited {
		t.Fatalf("lower package cap was overwritten: %+v", got)
	}
}

func TestRoleQuotaDayRemainingUsesConfiguredDayWindow(t *testing.T) {
	remaining, unlimited := roleQuotaDayRemaining(UserRoleQuota{Enabled: 1, Day: 100}, 120)
	if unlimited || remaining != 0 {
		t.Fatalf("exhausted role day remaining = %d,%v", remaining, unlimited)
	}
	if _, unlimited := roleQuotaDayRemaining(UserRoleQuota{Enabled: 0, Day: 100}, 0); !unlimited {
		t.Fatal("disabled role quota must stay unlimited")
	}
}

// 控制台展示的“当前套餐”必须跟着用户选择的主套餐走，否则页面显示 A、
// 网关实际扣 B。
func TestWorkbenchPackageQueriesPreferPrimaryPackage(t *testing.T) {
	for name, item := range map[string]struct {
		query string
		order string
	}{
		"member":    {workbenchMemberPackageQuery, "order by is_primary desc, end_at asc, id asc"},
		"interface": {workbenchInterfacePackagesQuery, "order by p.is_primary desc, p.end_at asc, p.id asc"},
	} {
		if !strings.Contains(item.query, item.order) {
			t.Fatalf("%s package query must follow gateway deduction order %q: %s", name, item.order, item.query)
		}
	}
}

func TestApplyWorkbenchRoleCapKeepsPackageConfig(t *testing.T) {
	base := workbenchQuotaSummary{DailyLimit: 160000, RemainingCalls: 90000, FromPackage: true}
	got := applyWorkbenchRoleCap(base, UserRoleQuota{Enabled: 1, Day: 86400, CountMode: "attempt"}, 1000)
	if got != base {
		t.Fatalf("package config was capped by role day: %+v", got)
	}
	roleBase := workbenchQuotaSummary{DailyLimitUnlimited: true, RemainingCalls: 80}
	capped := applyWorkbenchRoleCap(roleBase, UserRoleQuota{Enabled: 1, Day: 86400}, 100)
	if capped.DailyLimit != 86400 || capped.DailyLimitUnlimited {
		t.Fatalf("role cap should still apply without package: %+v", capped)
	}
}
