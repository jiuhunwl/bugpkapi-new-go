package riskcontrol

import (
	"strconv"
	"strings"
	"testing"
)

func TestSanitizeSampleMasksSensitiveValues(t *testing.T) {
	sample := `{"token":"secret-token","password":"long secret phrase","email":"user@example.com","normal":"same-param"}&apiKey=live-secret Authorization=Bearer abc.def Cookie: session=raw-session url=https://api-user:url-password@example.com/path?token=url-secret`

	got := sanitizeSample(sample)

	for _, secret := range []string{"secret-token", "long secret phrase", "secret phrase", "user@example.com", "live-secret", "abc.def", "raw-session", "api-user", "url-password", "url-secret"} {
		if strings.Contains(got, secret) {
			t.Fatalf("sanitizeSample leaked %q in %q", secret, got)
		}
	}
	if !strings.Contains(got, "same-param") {
		t.Fatalf("sanitizeSample should keep non-sensitive request shape, got %q", got)
	}
}

func TestClassifyProxyPoolEventHasHigherRisk(t *testing.T) {
	severity, score := classifyEvent(RiskEvent{
		EventType:     "PROXY_POOL_SAME_PARAMS",
		RequestCount:  36,
		UniqueIPCount: 9,
	})

	if severity != "HIGH" && severity != "CRITICAL" {
		t.Fatalf("expected proxy pool event to be high risk, got %s with score %d", severity, score)
	}
	if score < 60 {
		t.Fatalf("expected score >= 60, got %d", score)
	}
}

func TestClassifyDistributedEventIsCriticalAtThreshold(t *testing.T) {
	severity, score := classifyEvent(RiskEvent{
		EventType:     "DISTRIBUTED_SAME_PARAMS",
		RequestCount:  minDistributedRequests,
		UniqueIPCount: minDistributedIPs,
	})

	if severity != "CRITICAL" {
		t.Fatalf("expected distributed event to be critical, got %s with score %d", severity, score)
	}
}

func TestNormalizeQueryBounds(t *testing.T) {
	got := normalizeQuery(RiskQuery{Limit: 9999, WindowMinutes: 9999})
	if got.Limit != maxLimit {
		t.Fatalf("expected limit capped at %d, got %d", maxLimit, got.Limit)
	}
	if got.WindowMinutes != maxWindowMinutes {
		t.Fatalf("expected window capped at %d, got %d", maxWindowMinutes, got.WindowMinutes)
	}
}

func TestOpenTrafficConditionExcludesAdminTestLogs(t *testing.T) {
	got := openTrafficCondition("l")
	if !strings.Contains(got, "l.charge_type") {
		t.Fatalf("expected aliased charge_type predicate, got %q", got)
	}
	if !strings.Contains(got, "<> 'ADMIN'") {
		t.Fatalf("expected ADMIN management-test logs to be excluded, got %q", got)
	}
	if strings.Contains(strings.ToLower(got), "coalesce(") {
		t.Fatalf("open traffic predicate should keep charge_type indexable, got %q", got)
	}
	if strings.ContainsAny(got, ";?") {
		t.Fatalf("open traffic predicate should stay a static SQL fragment, got %q", got)
	}

	plain := openTrafficCondition("")
	if strings.Contains(plain, "l.charge_type") || !strings.Contains(plain, "charge_type") {
		t.Fatalf("unexpected unaliased predicate: %q", plain)
	}
}

func TestRiskFingerprintColumnUsesDedicatedPersistedFingerprint(t *testing.T) {
	plain := riskFingerprintColumn("")
	aliased := riskFingerprintColumn("l")
	if plain != "request_fingerprint" || aliased != "l.request_fingerprint" {
		t.Fatalf("unexpected fingerprint columns: plain=%q aliased=%q", plain, aliased)
	}
	for _, value := range []string{plain, aliased} {
		lower := strings.ToLower(value)
		if strings.Contains(lower, "request_params") || strings.Contains(lower, "request_value") || strings.Contains(lower, "sha2") {
			t.Fatalf("risk detection must not derive equality from masked display fields: %q", value)
		}
	}
}

func TestProxyPoolGroupingSeparatesUnrelatedUsers(t *testing.T) {
	if !strings.Contains(proxyPoolGroupBy, "l.user_id") {
		t.Fatalf("proxy-pool grouping must include user identity: %q", proxyPoolGroupBy)
	}
	if !strings.Contains(proxyPoolGroupBy, "l.interface_id") || !strings.Contains(proxyPoolGroupBy, "param_signature") {
		t.Fatalf("proxy-pool grouping lost API or request fingerprint dimensions: %q", proxyPoolGroupBy)
	}
	if !strings.Contains(proxyPoolGroupBy, "unix_timestamp(l.created_at)") {
		t.Fatalf("proxy-pool grouping lost the fixed burst window: %q", proxyPoolGroupBy)
	}
}

func TestDistributedGroupingCorrelatesAcrossUsersAndKeys(t *testing.T) {
	if strings.Contains(distributedGroupBy, "l.user_id") || strings.Contains(distributedGroupBy, "l.api_key_id") {
		t.Fatalf("distributed grouping must correlate across identities: %q", distributedGroupBy)
	}
	for _, fragment := range []string{"l.interface_id", "param_signature", "unix_timestamp(l.created_at)"} {
		if !strings.Contains(distributedGroupBy, fragment) {
			t.Fatalf("distributed grouping lost %q: %q", fragment, distributedGroupBy)
		}
	}

	rules := strings.ToLower(riskDetectionCTEs())
	for _, fragment := range []string{
		"distributed_groups as",
		"count(distinct client_ip) >= " + strconv.Itoa(minDistributedIPs),
		"count(*) >= " + strconv.Itoa(minDistributedRequests),
		"inner join distributed_groups",
	} {
		if !strings.Contains(rules, fragment) {
			t.Fatalf("distributed risk rules lost %q: %s", fragment, rules)
		}
	}
	distributedStart := strings.Index(rules, "distributed_groups as")
	if distributedStart < 0 {
		t.Fatal("distributed risk group is missing")
	}
	distributedRules := rules[distributedStart:]
	if strings.Contains(distributedRules, "where user_id is not null") || strings.Contains(distributedRules, "count(distinct user_id) >=") {
		t.Fatalf("distributed detection must include anonymous and invalid-key traffic: %s", distributedRules)
	}
}

func TestRiskEventQueriesKeepLogsForDeletedInterfaces(t *testing.T) {
	join := strings.ToLower(riskInterfaceJoin)
	if !strings.Contains(join, "left join bp_interface_api") || strings.Contains(join, "inner join") {
		t.Fatalf("risk event interface join must retain deleted-interface logs: %q", riskInterfaceJoin)
	}
	for name, expression := range map[string]string{
		"api code": riskAPICode,
		"name":     riskInterfaceName,
	} {
		lower := strings.ToLower(expression)
		if !strings.Contains(lower, "l.api_code") {
			t.Fatalf("risk event %s fallback lost the call-log snapshot: %q", name, expression)
		}
	}
	if !strings.Contains(strings.ToLower(riskInterfaceName), "l.interface_id") {
		t.Fatalf("risk event name must still identify legacy logs without an api-code snapshot: %q", riskInterfaceName)
	}
}

func TestRiskTimeBucketUsesFixedFiveMinuteWindow(t *testing.T) {
	expression := riskTimeBucketExpression("l")
	if riskBurstBucketMinutes != 5 || !strings.Contains(expression, "/ 300") {
		t.Fatalf("risk time bucket = %q with %d minutes", expression, riskBurstBucketMinutes)
	}
}

func TestRiskSummaryDeduplicatesOverlappingRollupContributors(t *testing.T) {
	query := strings.ToLower(riskSummaryQuery())
	if !strings.Contains(query, "union") || strings.Contains(query, "union all") {
		t.Fatalf("risk summary must deduplicate overlapping rollup contributors: %s", query)
	}
	if !strings.Contains(query, "select sum(call_count) from risky_contributors") {
		t.Fatalf("risk summary lost de-duplicated risky request counting: %s", query)
	}
	if strings.Contains(query, "bp_interface_call_log") {
		t.Fatalf("risk summary still scans raw call logs: %s", query)
	}
}

func TestRiskDetectionQueriesShareTheSameRuleCTEs(t *testing.T) {
	rules := strings.TrimSpace(strings.ToLower(riskRollupDetectionCTEs()))
	if rules == "" {
		t.Fatal("shared risk detection CTEs are empty")
	}
	for name, query := range map[string]string{
		"earth":   riskEarthQuery(),
		"summary": riskSummaryQuery(),
		"trend":   riskTrendQuery(),
	} {
		if !strings.Contains(strings.ToLower(query), rules) {
			t.Fatalf("%s query does not use the shared risk rules", name)
		}
	}
}

// A grouping that does not start at the index prefix cannot be streamed, so
// MySQL materialises the whole 24-hour window into a temporary table before it
// can count anything. Putting bucket_start first, followed by the columns in
// index order, is what keeps the detector readable by an index scan; the
// distributed grouping alone went from 5.98s to 1.83s on production data once
// it matched its index prefix.
func TestRiskDetectionGroupingsMatchTheirIndexPrefixes(t *testing.T) {
	rules := strings.ToLower(riskRollupDetectionCTEs())
	for _, test := range []struct {
		grouping string
		columns  string
	}{
		{grouping: "single_groups", columns: "bucket_start,client_ip_hash,interface_id,request_fingerprint"},
		{grouping: "proxy_groups", columns: "bucket_start,user_id,interface_id,request_fingerprint"},
		{grouping: "distributed_groups", columns: "bucket_start,interface_id,request_fingerprint"},
	} {
		if !strings.Contains(rules, "group by "+test.columns) {
			t.Fatalf("%s must group by its index prefix %q: %s", test.grouping, test.columns, rules)
		}
	}
}

func TestRiskEarthUsesDeduplicatedRollupsAndWindowTotals(t *testing.T) {
	query := strings.ToLower(riskEarthQuery())
	for _, fragment := range []string{
		"from bp_call_region_ip_hour r force index(idx_bp_call_region_ip_time)",
		"from risky_contributors",
		"sum(call_count) risk_hits",
		"left join risk_regions rr",
		"count(distinct r.client_ip_hash) unique_ips",
		"count(*) over() active_points",
		"sum(call_count) over() total_calls",
		"sum(risk_hits) over() total_risk_hits",
	} {
		if !strings.Contains(query, fragment) {
			t.Fatalf("earth query lost %q: %s", fragment, query)
		}
	}
	if strings.Contains(query, "bp_call_region_hour r") || strings.Contains(query, "sum(r.unique_ip_count)") {
		t.Fatalf("risk-classified earth points must not use the additive unique-IP estimate: %s", query)
	}
}

func TestRiskOverviewEarthPointCountUsesCompactRegionRollups(t *testing.T) {
	query := strings.ToLower(riskOverviewMetricsQuery())
	for _, fragment := range []string{
		"from bp_call_region_hour r force index(idx_bp_call_region_hour_time)",
		"group by r.country,r.province,r.city,r.isp,r.latitude,r.longitude",
		"count(*) earth_points",
	} {
		if !strings.Contains(query, fragment) {
			t.Fatalf("risk overview metrics query lost %q: %s", fragment, query)
		}
	}
	if strings.Contains(query, "bp_call_region_ip_hour") {
		t.Fatalf("risk overview earth point count still uses the high-cardinality region/IP rollup: %s", query)
	}
	// The overview's global unique-IP figure intentionally remains exact and
	// therefore continues to come from the risk fingerprint table.
	if !strings.Contains(query, "count(distinct case when f.has_client_ip=1 then f.client_ip_hash end)") {
		t.Fatalf("risk overview lost its exact fingerprint-based unique-IP count: %s", query)
	}
}

func TestRiskTrendCountsDeduplicatedRollupContributorsIndependently(t *testing.T) {
	query := strings.ToLower(riskTrendQuery())
	if !strings.Contains(query, "risky_contributors as") || !strings.Contains(query, "union") || strings.Contains(query, "union all") {
		t.Fatalf("risk trend must deduplicate overlapping rollup contributors: %s", query)
	}
	if !strings.Contains(query, "sum(call_count) risky_requests") {
		t.Fatalf("risk trend lost risky request aggregation: %s", query)
	}
	if strings.Contains(query, "bp_interface_call_log") {
		t.Fatalf("risk trend still scans raw call logs: %s", query)
	}
	if strings.Contains(query, "limit") {
		t.Fatalf("risk trend must not depend on event display pagination: %s", query)
	}
}

func TestOverviewAnalyticsSharesOneRiskDetectionScan(t *testing.T) {
	query := strings.ToLower(riskOverviewAnalyticsQuery())
	for _, fragment := range []string{
		"from bp_call_risk_fingerprint_5m f",
		"f.bucket_start >= from_unixtime(",
		"single_events",
		"distributed_events",
		"select sum(call_count) from risky_contributors",
		"group by trend_bucket",
		"cross join summary",
	} {
		if !strings.Contains(query, fragment) {
			t.Fatalf("overview analytics lost %q: %s", fragment, query)
		}
	}
	// 2026-09-05 生产事故修复（FIXES.md 修复项 3）移除了 force index：
	// idx_bp_call_risk_distributed 不以 bucket_start 前导，强制使用它会让
	// 时间窗口聚合退化成大范围扫描，MySQL CPU 曾被打到 190%。
	// 这里锁定「不得重新引入强制索引」，交给优化器自行选择。
	if strings.Contains(query, "force index") {
		t.Fatalf("overview analytics reintroduced a force index hint: %s", query)
	}
	if strings.Count(query, "from bp_call_risk_fingerprint_5m") != 1 {
		t.Fatalf("overview summary and trend must share one rollup snapshot: %s", query)
	}
	if strings.Contains(query, "bp_interface_call_log") {
		t.Fatalf("overview analytics still scans raw call logs: %s", query)
	}
}

func TestOverviewAnalyticsUsesNarrowCoveringRiskBase(t *testing.T) {
	query := strings.ToLower(riskOverviewAnalyticsQuery())
	riskBaseIndex := strings.Index(query, "risk_base as")
	if riskBaseIndex < 0 {
		t.Fatalf("overview analytics has no risk_base CTE: %s", query)
	}
	base := query[:riskBaseIndex]
	for _, required := range []string{
		"f.bucket_start",
		"f.interface_id",
		"f.request_fingerprint",
		"f.user_id",
		"f.api_key_id",
		"f.client_ip_hash",
		"f.has_client_ip",
		"f.call_count",
	} {
		if !strings.Contains(base, required) {
			t.Fatalf("overview analytics covering base lost %q: %s", required, base)
		}
	}
	for _, unnecessary := range []string{
		"f.client_ip,",
		"f.api_code",
		"f.country",
		"f.province",
		"f.city",
		"f.elapsed_sum",
		"f.first_seen",
		"f.last_seen",
	} {
		if strings.Contains(base, unnecessary) {
			t.Fatalf("overview analytics still reads non-covering field %q: %s", unnecessary, base)
		}
	}
}

func TestRiskEventQueriesFilterCandidatesBeforeLoadingEvidence(t *testing.T) {
	tests := []struct {
		groupBy string
		kind    string
	}{
		{kind: "single", groupBy: "group by f.bucket_start,f.client_ip_hash,f.interface_id,f.request_fingerprint"},
		{kind: "proxy", groupBy: "group by f.bucket_start,f.user_id,f.interface_id,f.request_fingerprint"},
		{kind: "distributed", groupBy: "group by f.bucket_start,f.interface_id,f.request_fingerprint"},
	}
	for _, test := range tests {
		t.Run(test.kind, func(t *testing.T) {
			query := strings.ToLower(riskRollupEventsQuery(test.kind))
			candidateIndex := strings.Index(query, "detected_groups as")
			evidenceIndex := strings.Index(query, "group_concat")
			if candidateIndex < 0 || evidenceIndex < 0 || candidateIndex >= evidenceIndex {
				t.Fatalf("%s event query must detect bounded candidates before evidence aggregation: %s", test.kind, query)
			}
			for _, fragment := range []string{
				test.groupBy,
				"inner join detected_groups g",
				"limit ?",
			} {
				if !strings.Contains(query, fragment) {
					t.Fatalf("%s event query lost %q: %s", test.kind, fragment, query)
				}
			}
			// 同 TestOverviewAnalyticsSharesOneRiskDetectionScan：2026-09-05
			// 事故修复移除了各检测类型的 force index，不得重新引入。
			if strings.Contains(query, "force index") {
				t.Fatalf("%s event query reintroduced a force index hint: %s", test.kind, query)
			}
		})
	}
}

func TestRiskTrendBucketKeepsChartSeriesBounded(t *testing.T) {
	tests := []struct {
		format        string
		seconds       int
		windowMinutes int
	}{
		{format: "%H:%i", seconds: 60, windowMinutes: 10},
		{format: "%H:%i", seconds: 300, windowMinutes: 360},
		{format: "%m-%d %H:%i", seconds: 900, windowMinutes: 1440},
	}
	for _, test := range tests {
		seconds, format := riskTrendBucket(test.windowMinutes)
		if seconds != test.seconds || format != test.format {
			t.Fatalf("trend bucket for %d minutes = %d/%q, want %d/%q", test.windowMinutes, seconds, format, test.seconds, test.format)
		}
		if test.windowMinutes*60/seconds > 100 {
			t.Fatalf("trend bucket for %d minutes produces more than 100 points", test.windowMinutes)
		}
	}
}

func TestDetectionPolicyMatchesRiskRules(t *testing.T) {
	policy := detectionPolicy()
	if policy.BurstWindowMinutes != riskBurstBucketMinutes ||
		policy.SingleIPRequests != minSingleIPRequests ||
		policy.ProxyPoolRequests != minProxyRequests ||
		policy.ProxyPoolIPs != minProxyIPs ||
		policy.DistributedRequests != minDistributedRequests ||
		policy.DistributedIPs != minDistributedIPs {
		t.Fatalf("detection policy drifted from SQL thresholds: %+v", policy)
	}
}

func TestHomeRiskPointsOnlyReturnsAggregatedLocations(t *testing.T) {
	query := strings.ToLower(riskHomePointsQuery())
	for _, fragment := range []string{
		"from risky_contributors",
		"sum(call_count) risk_hits",
		"from bp_call_region_hour r force index(idx_bp_call_region_hour_time)",
		"inner join risk_regions rr",
		"group by r.country,r.province,r.city",
	} {
		if !strings.Contains(query, fragment) {
			t.Fatalf("home risk query lost %q: %s", fragment, query)
		}
	}
	if strings.Contains(query, "bp_call_region_ip_hour") {
		t.Fatalf("home risk query still depends on the high-cardinality region/IP rollup: %s", query)
	}
	selectIndex := strings.LastIndex(query, "select ")
	fromIndex := strings.Index(query[selectIndex:], "\n\t\tfrom ")
	if selectIndex < 0 || fromIndex < 0 {
		t.Fatal("home risk query has no final select")
	}
	finalSelect := query[selectIndex : selectIndex+fromIndex]
	for _, sensitive := range []string{"client_ip", "api_key_id", "user_id", "request_fingerprint"} {
		if strings.Contains(finalSelect, sensitive) {
			t.Fatalf("public home risk query exposed %s: %s", sensitive, finalSelect)
		}
	}
}

func TestRiskEventSampleUsesOnlyIrreversibleFingerprint(t *testing.T) {
	expression := riskRequestSampleExpression("l")
	lower := strings.ToLower(expression)
	for _, sensitive := range []string{"request_params", "request_value"} {
		if strings.Contains(lower, sensitive) {
			t.Fatalf("risk event sample must not read raw %s: %s", sensitive, expression)
		}
	}
	for _, fragment := range []string{"fingerprint:", "l.request_fingerprint", "left(", "12"} {
		if !strings.Contains(lower, fragment) {
			t.Fatalf("risk event sample lost fingerprint-only display fragment %q: %s", fragment, expression)
		}
	}
}

func TestRiskEvidenceSamplesAreBoundedAndParsed(t *testing.T) {
	accounts := parseRiskAccountSamples("7:alice|8:bob|7:alice|9:")
	if len(accounts) != 3 {
		t.Fatalf("account evidence count = %d, want 3: %#v", len(accounts), accounts)
	}
	if accounts[0].ID != "7" || accounts[0].Username != "alice" {
		t.Fatalf("first account evidence = %#v", accounts[0])
	}
	if accounts[2].Username != "user-9" {
		t.Fatalf("missing username fallback = %#v", accounts[2])
	}

	values := make([]string, 0, maxRiskEvidenceItems+4)
	for index := 0; index < maxRiskEvidenceItems+4; index++ {
		values = append(values, "203.0.113."+strconv.Itoa(index+1))
	}
	got := splitRiskEvidenceSamples(strings.Join(values, "|"))
	if len(got) != maxRiskEvidenceItems {
		t.Fatalf("evidence samples are not bounded: got %d, want %d", len(got), maxRiskEvidenceItems)
	}
}

func TestRiskEventQueriesIncludeSafeEvidenceFields(t *testing.T) {
	query := strings.ToLower(riskEvidenceSelect("l"))
	for _, fragment := range []string{
		"account_samples",
		"request_ips",
		"api_key_samples",
		"request_methods",
		"success_count",
		"failed_count",
	} {
		if !strings.Contains(query, fragment) {
			t.Fatalf("risk evidence projection lost fragment %q: %s", fragment, query)
		}
	}
	if !strings.Contains(strings.ToLower(riskUserJoin), "left join bp_user u on u.id = l.user_id") {
		t.Fatalf("risk evidence join lost account lookup: %q", riskUserJoin)
	}
}

func TestFailureRankingQueriesAggregateCandidatesWithoutRepeatingRiskDetection(t *testing.T) {
	queries := map[string]string{
		"ip":      failedIPRankingsQuery(),
		"account": failedAccountRankingsQuery(),
	}
	rules := strings.TrimSpace(strings.ToLower(riskRollupDetectionCTEs()))
	for name, query := range queries {
		lower := strings.ToLower(query)
		if !strings.Contains(lower, "sum(f.failure_count)") {
			t.Fatalf("%s ranking lost failed-call aggregation: %s", name, query)
		}
		if strings.Contains(lower, "bp_interface_call_log") {
			t.Fatalf("%s ranking still scans raw call logs: %s", name, query)
		}
		if strings.Contains(lower, rules) || strings.Contains(lower, "risky_contributors") || strings.Contains(lower, "bp_call_risk_fingerprint_5m") {
			t.Fatalf("%s ranking still repeats the expensive risk detector: %s", name, query)
		}
		if !strings.Contains(lower, "0 risk_hits") {
			t.Fatalf("%s ranking must leave risk enrichment to the shared analytics query: %s", name, query)
		}
		if strings.Contains(lower[strings.LastIndex(lower, "select "):], "request_params") ||
			strings.Contains(lower[strings.LastIndex(lower, "select "):], "request_value") {
			t.Fatalf("%s ranking exposed raw request fields", name)
		}
		for _, fragment := range []string{
			"interface_failure_totals as",
			"interface_failed_calls",
			"row_number() over",
			"rank_no=1",
		} {
			if !strings.Contains(lower, fragment) {
				t.Fatalf("%s ranking lost associated-interface evidence %q: %s", name, fragment, query)
			}
		}
	}
	ipQuery := strings.ToLower(failedIPRankingsQuery())
	if !strings.Contains(ipQuery, "group by f.client_ip") {
		t.Fatal("IP ranking must group by client IP")
	}
	for _, fragment := range []string{
		"max(nullif(f.country,''))",
		"max(nullif(f.province,''))",
		"max(nullif(f.city,''))",
	} {
		if !strings.Contains(ipQuery, fragment) {
			t.Fatalf("IP ranking region fallback is not ONLY_FULL_GROUP_BY safe: missing %q", fragment)
		}
	}
	if !strings.Contains(strings.ToLower(failedAccountRankingsQuery()), "group by f.user_id") {
		t.Fatal("account ranking must group by user id")
	}
}

func TestOverviewAnalyticsEnrichesOnlySelectedFailureCandidates(t *testing.T) {
	query := strings.ToLower(riskOverviewAnalyticsQueryForCandidates(2, 2))
	for _, fragment := range []string{
		"candidate_ips(client_ip,client_ip_hash) as",
		"candidate_users(user_id) as",
		"ip_stats as",
		"user_stats as",
		"select 'ip',null",
		"select 'user',null",
		"left join risky_contributors r",
	} {
		if !strings.Contains(query, fragment) {
			t.Fatalf("candidate analytics lost %q: %s", fragment, query)
		}
	}
	if strings.Count(query, "from bp_call_risk_fingerprint_5m") != 1 {
		t.Fatalf("candidate enrichment must share one risk-rollup scan: %s", query)
	}
	if got := strings.Count(query, "?"); got != 11 {
		t.Fatalf("candidate analytics placeholders=%d, want 11: %s", got, query)
	}
}

func TestRollupQueryParameterCountsMatchRepositoryCalls(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  int
	}{
		{name: "failed IP rankings", query: failedIPRankingsQuery(), want: 3},
		{name: "failed account rankings", query: failedAccountRankingsQuery(), want: 3},
		{name: "earth", query: riskEarthQuery(), want: 3},
		{name: "home risk points", query: riskHomePointsQuery(), want: 3},
		{name: "single-IP events", query: riskRollupEventsQuery("single"), want: 3},
		{name: "proxy-pool events", query: riskRollupEventsQuery("proxy"), want: 4},
		{name: "distributed events", query: riskRollupEventsQuery("distributed"), want: 4},
	}
	for _, test := range tests {
		if got := strings.Count(test.query, "?"); got != test.want {
			t.Fatalf("%s query placeholders = %d, want %d: %s", test.name, got, test.want, test.query)
		}
	}
}

func TestOverviewRiskScoreIncludesBoundedInvalidKeyPressure(t *testing.T) {
	if got := overviewRiskScore(Overview{InvalidKeyAttempts: 9}); got != 0 {
		t.Fatalf("low invalid-key noise changed risk score: %d", got)
	}
	if got := overviewRiskScore(Overview{InvalidKeyAttempts: 30, InvalidKeyBlocked: 4}); got != 18 {
		t.Fatalf("invalid-key pressure score = %d, want 18", got)
	}
	if got := overviewRiskScore(Overview{InvalidKeyAttempts: 1000, InvalidKeyBlocked: 1000}); got != 50 {
		t.Fatalf("invalid-key score cap = %d, want 50", got)
	}
	if got := overviewRiskScore(Overview{DistributedEvents: 4, InvalidKeyAttempts: 1000, InvalidKeyBlocked: 1000}); got != 100 {
		t.Fatalf("combined risk score cap = %d, want 100", got)
	}
}
