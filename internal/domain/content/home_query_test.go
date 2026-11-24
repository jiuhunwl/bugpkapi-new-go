package content

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestHomeHotAPIsQueryUsesDailyActiveUserRollups(t *testing.T) {
	query := strings.ToLower(homeHotAPIsQuery())
	for _, fragment := range []string{
		"from bp_call_api_metric_day d force index (primary)",
		"d.bucket_date>=date_sub(current_date(), interval 29 day)",
		"inner join bp_user u on u.id=d.user_id",
		"case when u.status = 0",
		"cast(coalesce(traffic.call_count,0) as unsigned)",
	} {
		if !strings.Contains(query, fragment) {
			t.Fatalf("home hot API query lost %q: %s", fragment, query)
		}
	}
	if strings.Contains(query, "bp_interface_call_log") {
		t.Fatalf("home hot API query still scans raw call logs: %s", query)
	}
	if strings.Contains(query, "bp_call_api_metric_minute") {
		t.Fatalf("home hot API query still scans minute rollups: %s", query)
	}
}

func TestHomeOverviewRequestPathNeverFallsBackToRawCallLog(t *testing.T) {
	contents, err := os.ReadFile("repository.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(contents)
	start := strings.Index(source, "func (r *Repository) HomeOverview")
	end := strings.Index(source, "func homeTrafficStatsQuery")
	if start < 0 || end <= start {
		t.Fatalf("cannot isolate HomeOverview implementation")
	}
	function := source[start:end]
	for _, forbidden := range []string{"openTrafficRollupLagging", "homeTrafficStatsLogQuery", "bp_interface_call_log"} {
		if strings.Contains(function, forbidden) {
			t.Fatalf("homepage request path contains raw-log fallback %q: %s", forbidden, function)
		}
	}
}

func TestHomeSnapshotMigrationContract(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test path")
	}
	path := filepath.Join(filepath.Dir(filename), "..", "..", "..", "migrations", "000089_home_overview_performance.up.sql")
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sqlText := strings.ToLower(string(contents))
	for _, required := range []string{
		"create table if not exists `bp_call_api_metric_day`",
		"primary key (`bucket_date`, `interface_id`, `user_id`)",
		"from `bp_call_api_metric_minute` force index (`primary`)",
		"create table if not exists `bp_home_overview_snapshot`",
		"`payload` json not null",
		"`source_updated_at` datetime(6) default null",
		"primary key (`id`)",
		"insert into `bp_home_overview_snapshot`",
	} {
		if !strings.Contains(sqlText, required) {
			t.Fatalf("home snapshot migration missing %q", required)
		}
	}
}

func TestHomeOverviewHTTPAssemblyReadsPersistedAnalytics(t *testing.T) {
	contents, err := os.ReadFile("service.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(contents)
	start := strings.Index(source, "func (s *Service) buildHomeOverview")
	end := strings.Index(source, "func (s *Service) ValidateNoticeUpload")
	if start < 0 || end <= start {
		t.Fatal("cannot isolate buildHomeOverview implementation")
	}
	function := source[start:end]
	if !strings.Contains(function, "LoadHomeAnalyticsSnapshot") {
		t.Fatalf("homepage HTTP assembly does not load the persisted analytics snapshot: %s", function)
	}
	if strings.Contains(function, "s.repo.HomeOverview(") {
		t.Fatalf("homepage HTTP assembly still performs live analytics: %s", function)
	}
}

func TestPlatformStatsDoesNotExposeUnstableAllHistoryRequestTotal(t *testing.T) {
	raw, err := json.Marshal(PlatformStats{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "requestTotal") {
		t.Fatalf("platform stats still exposes cleanup-sensitive requestTotal: %s", raw)
	}
}

func TestHomeTrafficStatsQueryCombinesBoundedRollupWindows(t *testing.T) {
	query := strings.ToLower(homeTrafficStatsQuery())
	for _, fragment := range []string{
		"with active_minute as",
		"from bp_call_user_metric_minute m force index (primary)",
		"m.bucket_start>=date_sub(now(), interval 48 hour)",
		"from bp_call_region_hour r force index (idx_bp_call_region_hour_time)",
		"r.bucket_start>=date_sub(now(), interval 24 hour)",
		"select count(distinct case",
		"when nullif(r.province,'') is not null then concat('province:',r.province)",
		"case when m.bucket_start>=date_sub(now(), interval 24 hour)",
		"case when m.bucket_start<date_sub(now(), interval 24 hour)",
		"ceil(max(case when m.bucket_start>=date_sub(now(), interval 24 hour) then m.call_count else 0 end)/60)",
		"case when u.status = 0",
		"case when region_user.status = 0",
	} {
		if !strings.Contains(query, fragment) {
			t.Fatalf("home traffic stats query lost %q: %s", fragment, query)
		}
	}
	if strings.Contains(query, "bp_interface_call_log") {
		t.Fatalf("home traffic stats still scans raw call logs: %s", query)
	}
	if strings.Contains(query, "bp_call_region_ip_hour") {
		t.Fatalf("home traffic stats still scans the high-cardinality region/IP rollup: %s", query)
	}
}
