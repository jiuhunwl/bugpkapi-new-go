package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"
)

func TestSanitizeExitMessageRedactsNetworkEndpoints(t *testing.T) {
	t.Parallel()
	input := `request failed: Get "https://parse.upstream.example.com/api/parse?url=https%3A%2F%2Fv.kuaishou.com%2FAbCdEf12": net/http: timeout awaiting response headers`
	got := sanitizeExitMessage("%s", input)
	for _, forbidden := range []string{"parse.upstream.example.com", "v.kuaishou.com", "/api/parse", "AbCdEf12"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("CLI error leaked endpoint detail %q: %s", forbidden, got)
		}
	}
	if !strings.Contains(got, "timeout awaiting response headers") || !strings.Contains(got, "[UPSTREAM_REDACTED]") {
		t.Fatalf("CLI error lost safe diagnostic context: %s", got)
	}
}

func TestPrintSafetyBannerDoesNotExposeConnectionDetails(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	printSafetyBanner(&output, databaseTarget{
		Name: "bugpk_analytics_bench",
		Host: "db.internal.example:3306",
		User: "benchmark_admin",
	}, options{Rows: 1, Batch: 1, Workers: 1})
	got := output.String()
	for _, forbidden := range []string{"db.internal.example", "3306", "benchmark_admin"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("safety banner leaked connection detail %q: %s", forbidden, got)
		}
	}
	if !strings.Contains(got, "host=[UPSTREAM_REDACTED]") || !strings.Contains(got, "user=[REDACTED]") {
		t.Fatalf("safety banner lost redaction placeholders: %s", got)
	}
}

func TestValidateTargetRejectsApplicationAndSystemDatabases(t *testing.T) {
	t.Parallel()
	for _, dsn := range []string{
		"root:root@tcp(127.0.0.1:3306)/bugpk_api?parseTime=true",
		"root:root@tcp(127.0.0.1:3306)/BUGPK_API?parseTime=true",
		"root:root@tcp(127.0.0.1:3306)/mysql?parseTime=true",
		"root:root@tcp(127.0.0.1:3306)/information_schema?parseTime=true",
		"root:root@tcp(127.0.0.1:3306)/?parseTime=true",
	} {
		if target, err := validateTarget(dsn); err == nil {
			t.Fatalf("validateTarget(%q) unexpectedly accepted %+v", dsn, target)
		}
	}
}

func TestValidateTargetRejectsDatabaseWithoutIsolationMarker(t *testing.T) {
	t.Parallel()
	if target, err := validateTarget("root:root@tcp(127.0.0.1:3306)/customer_data?parseTime=true"); err == nil {
		t.Fatalf("validateTarget unexpectedly accepted %+v", target)
	}
}

func TestValidateTargetAcceptsExplicitIsolatedDatabase(t *testing.T) {
	t.Parallel()
	target, err := validateTarget("bench:secret@tcp(127.0.0.1:3306)/bugpk_analytics_bench?parseTime=true&loc=Local")
	if err != nil {
		t.Fatal(err)
	}
	if target.Name != "bugpk_analytics_bench" || target.User != "bench" || target.Host != "127.0.0.1:3306" {
		t.Fatalf("unexpected target: %+v", target)
	}
}

func TestParseOptionsDefaultsAndBounds(t *testing.T) {
	t.Parallel()
	getenv := func(name string) string {
		if name == "BUGPK_ANALYTICS_BENCH_DSN" {
			return "root:root@tcp(localhost:3306)/bugpk_analytics_bench?parseTime=true"
		}
		return ""
	}
	opts, err := parseOptions(nil, getenv)
	if err != nil {
		t.Fatal(err)
	}
	if opts.Rows != 1_000_000 || opts.Batch != 1_000 || opts.Workers != 4 {
		t.Fatalf("unexpected defaults: %+v", opts)
	}
	if opts.RollupBatch != 20_000 || opts.QueryTimeout != 30*time.Second || !opts.ExplainAnalyze {
		t.Fatalf("unexpected analytics defaults: %+v", opts)
	}
	if opts.MigrationPath != "migrations/000090_analytics_rollup_repair.up.sql" {
		t.Fatalf("unexpected default migration: %q", opts.MigrationPath)
	}

	invalid := [][]string{
		{"-rows", "0"},
		{"-rows", "1000000001"},
		{"-batch", "2001"},
		{"-workers", "65"},
		{"-rollup-batch", "20001"},
		{"-rollup-batches", "0"},
		{"-query-timeout", "0s"},
	}
	for _, args := range invalid {
		if _, err := parseOptions(args, getenv); err == nil {
			t.Fatalf("parseOptions(%v) unexpectedly succeeded", args)
		}
	}
}

func TestParseOptionsRequiresDSN(t *testing.T) {
	t.Parallel()
	if _, err := parseOptions(nil, func(string) string { return "" }); err == nil || !strings.Contains(err.Error(), "DSN") {
		t.Fatalf("expected missing DSN error, got %v", err)
	}
}

func TestEstimateStorageMakesScaleExplicit(t *testing.T) {
	t.Parallel()
	oneMillion := estimateStorage(1_000_000)
	trillion := estimateStorage(1_000_000_000_000)
	if oneMillion.RawBytes != 800_000_000 || oneMillion.WithIndexesBytes != 1_800_000_000 {
		t.Fatalf("unexpected million-row estimate: %+v", oneMillion)
	}
	if trillion.RawBytes < 700_000_000_000_000 || trillion.WithIndexesBytes < 1_000_000_000_000_000 {
		t.Fatalf("trillion-row estimate is misleadingly small: %+v", trillion)
	}
	if got := humanBytes(trillion.WithIndexesBytes); !strings.Contains(got, "PiB") {
		t.Fatalf("expected PiB-scale estimate, got %q", got)
	}
}

func TestSplitSQLStatementsSupportsMigration78(t *testing.T) {
	t.Parallel()
	script := "-- comment\nCREATE TABLE a(id int);\n\nINSERT INTO a VALUES (1);\n"
	statements := splitSQLStatements(script)
	if len(statements) != 2 || statements[0] != "CREATE TABLE a(id int)" || statements[1] != "INSERT INTO a VALUES (1)" {
		t.Fatalf("unexpected statements: %#v", statements)
	}
}

func TestSplitSQLStatementsKeepsQuotedSemicolons(t *testing.T) {
	t.Parallel()
	script := `
		CREATE TABLE a(
			name varchar(64) COMMENT 'first; second',
			label varchar(64) DEFAULT 'it''s; valid',
			` + "`odd;identifier`" + ` int
		);
		INSERT INTO a(name,label) VALUES ('a;b','c;d');
	`
	statements := splitSQLStatements(script)
	if len(statements) != 2 {
		t.Fatalf("statement count = %d, want 2: %#v", len(statements), statements)
	}
	if !strings.Contains(statements[0], "COMMENT 'first; second'") ||
		!strings.Contains(statements[0], "DEFAULT 'it''s; valid'") ||
		!strings.Contains(statements[0], "`odd;identifier`") {
		t.Fatalf("quoted semicolons were split: %q", statements[0])
	}
	if !strings.Contains(statements[1], "VALUES ('a;b','c;d')") {
		t.Fatalf("insert literal was split: %q", statements[1])
	}
}

func TestSyntheticDataIncludesChinaTaiwanHongKongAndMacao(t *testing.T) {
	t.Parallel()
	want := map[string]bool{"台湾省": false, "香港特别行政区": false, "澳门特别行政区": false}
	for i := int64(0); i < 64; i++ {
		country, province, _, _, _, _ := syntheticRegion(i)
		if _, ok := want[province]; ok {
			if country != "中国" {
				t.Fatalf("%s must use country 中国, got %q", province, country)
			}
			want[province] = true
		}
	}
	for province, found := range want {
		if !found {
			t.Fatalf("missing synthetic region %s", province)
		}
	}
}

func TestCallLogListQueriesCoverCoreShapes(t *testing.T) {
	t.Parallel()
	queries := callLogListQueries(mustTime(t))
	names := make(map[string]bool)
	for _, query := range queries {
		names[query.Name] = true
	}
	for _, want := range []string{
		"call_log_user_first_page", "call_log_user_interface_seek",
		"call_log_admin_offset_deep", "call_log_admin_api_key",
		"call_log_admin_trace", "call_log_admin_client_ip",
		"call_log_admin_keyword_fuzzy", "call_log_user_interface_count",
	} {
		if !names[want] {
			t.Fatalf("missing benchmark query %s", want)
		}
	}
}

func mustTime(t *testing.T) time.Time {
	t.Helper()
	parsed, err := time.Parse("2006-01-02 15:04:05", "2026-08-23 12:00:00")
	if err != nil {
		t.Fatalf("parse time: %v", err)
	}
	return parsed
}

func TestAccessSituationBenchmarkUsesCompactRegionRollup(t *testing.T) {
	t.Parallel()
	queries := analyticsQueries(time.Now())
	for _, query := range queries {
		if query.Name != "admin_access_situation_30d" {
			continue
		}
		lower := strings.ToLower(query.SQL)
		if !strings.Contains(lower, "from bp_call_region_hour") ||
			!strings.Contains(lower, "sum(r.unique_ip_count)") {
			t.Fatalf("access benchmark does not exercise compact rollup: %s", query.SQL)
		}
		if strings.Contains(lower, "bp_call_region_ip_hour") ||
			strings.Contains(lower, "count(distinct r.client_ip_hash)") {
			t.Fatalf("access benchmark still exercises high-cardinality exact rollup: %s", query.SQL)
		}
		return
	}
	t.Fatal("admin access benchmark query is missing")
}

func TestAnalyticsQueriesMeasurePersistedHomepageSnapshotRead(t *testing.T) {
	t.Parallel()
	for _, query := range analyticsQueries(time.Now()) {
		if query.Name != "homepage_snapshot_read" {
			continue
		}
		lower := strings.ToLower(query.SQL)
		if !strings.Contains(lower, "from bp_home_overview_snapshot") ||
			!strings.Contains(lower, "where id=1") {
			t.Fatalf("homepage snapshot benchmark reads the wrong shape: %s", query.SQL)
		}
		if strings.Contains(lower, "bp_interface_call_log") {
			t.Fatalf("homepage snapshot benchmark scans raw logs: %s", query.SQL)
		}
		return
	}
	t.Fatal("homepage snapshot read benchmark is missing")
}

func TestBenchmarkLifecycleIncludesHomeDailyRollup(t *testing.T) {
	contents, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(contents)
	sections := []struct {
		start string
		end   string
	}{
		{start: "func requireSchema", end: "func seedDimensions"},
		{start: "func printRollupCardinality", end: "func cleanupSynthetic"},
	}
	for _, section := range sections {
		start := strings.Index(source, section.start)
		if start < 0 {
			t.Fatalf("cannot find %s", section.start)
		}
		end := strings.Index(source[start+len(section.start):], section.end)
		if end < 0 {
			t.Fatalf("cannot find %s after %s", section.end, section.start)
		}
		body := source[start : start+len(section.start)+end]
		if !strings.Contains(body, "bp_call_api_metric_day") {
			t.Fatalf("%s does not include the homepage daily rollup", section.start)
		}
	}
}

func TestCleanupTruncatesOnlyAfterDedicatedDatabaseSafetyChecks(t *testing.T) {
	t.Parallel()
	want := map[string]bool{
		"bp_interface_call_log":       false,
		"bp_call_api_metric_day":      false,
		"bp_call_region_ip_hour":      false,
		"bp_call_risk_fingerprint_5m": false,
		"bp_home_overview_snapshot":   false,
	}
	for _, table := range syntheticCleanupTruncateTables {
		if _, ok := want[table]; ok {
			want[table] = true
		}
	}
	for table, found := range want {
		if !found {
			t.Fatalf("cleanup truncate list is missing %s", table)
		}
	}
}

func TestCountBenchmarkMatchesUnboundedExactTotalContract(t *testing.T) {
	for _, query := range callLogListQueries(time.Now()) {
		if query.Name == "call_log_user_interface_count" {
			if strings.Contains(strings.ToLower(query.SQL), "limit") {
				t.Fatalf("benchmark understates live exact-count cost: %s", query.SQL)
			}
			return
		}
	}
	t.Fatal("exact total benchmark missing")
}
