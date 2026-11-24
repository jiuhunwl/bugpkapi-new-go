package observability

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCallLogCountQuerySkipsLookupJoinsWithoutKeyword(t *testing.T) {
	params := CallLogListParams{InterfaceID: 12, StartTime: "2026-08-01 00:00:00"}
	where, _ := callLogWhere(params, true, 0)
	query := strings.ToLower(callLogCountQuery(params, where))

	for _, fragment := range []string{
		"select count(*)",
		"from bp_interface_call_log l",
	} {
		if !strings.Contains(query, fragment) {
			t.Fatalf("call-log count query lost %q: %s", fragment, query)
		}
	}
	for _, forbidden := range []string{"bp_interface_api", "bp_user", "order by"} {
		if strings.Contains(query, forbidden) {
			t.Fatalf("call-log count query unnecessarily contains %q: %s", forbidden, query)
		}
	}
}

func TestCallLogCountQueryKeepsLookupJoinsForKeywordSemantics(t *testing.T) {
	params := CallLogListParams{Keyword: "trace", StartTime: "2026-08-01 00:00:00"}
	where, _ := callLogWhere(params, true, 0)
	query := strings.ToLower(callLogCountQuery(params, where))
	for _, fragment := range []string{
		"select count(*)",
		"left join bp_interface_api a on a.id = l.interface_id",
		"left join bp_user u on u.id = l.user_id",
		"l.request_params like ?",
	} {
		if !strings.Contains(query, fragment) {
			t.Fatalf("keyword count query lost %q: %s", fragment, query)
		}
	}
}

func TestCallLogBillingOnlyFilterFollowsInterfaceBillingSwitches(t *testing.T) {
	params := CallLogListParams{BillingOnly: true}
	where, _ := callLogWhere(params, true, 0)
	for _, fragment := range []string{
		"l.billable=1",
		"a.billing_enabled=1",
		"a.billing_log_enabled=1",
	} {
		if !strings.Contains(where, fragment) {
			t.Fatalf("billing-only where lost %q: %s", fragment, where)
		}
	}
	countQuery := strings.ToLower(callLogCountQuery(params, where))
	if !strings.Contains(countQuery, "left join bp_interface_api a on a.id = l.interface_id") {
		t.Fatalf("billing-only count query must join the interface switches: %s", countQuery)
	}
	if strings.Contains(countQuery, "bp_user") {
		t.Fatalf("billing-only filter must not add the keyword user join: %s", countQuery)
	}
	listQuery := strings.ToLower(callLogListQuery(params, where, false))
	if !strings.Contains(listQuery, "left join bp_interface_api a on a.id = l.interface_id") {
		t.Fatalf("billing-only list page must resolve interface switches before wide rows: %s", listQuery)
	}
}

func TestCallLogDefaultViewDoesNotJoinInterfaceSwitches(t *testing.T) {
	params := CallLogListParams{InterfaceID: 12}
	where, _ := callLogWhere(params, true, 0)
	query := strings.ToLower(callLogCountQuery(params, where))
	if strings.Contains(query, "bp_interface_api") || strings.Contains(where, "billing_enabled") {
		t.Fatalf("plain call-log view must keep counting raw calls: %s %s", query, where)
	}
}

func TestCallLogCountQueryCountsAllMatchingRows(t *testing.T) {
	params := CallLogListParams{PageSize: 80}
	where, _ := callLogWhere(params, true, 0)
	query := strings.ToLower(callLogCountQuery(params, where))

	if strings.Contains(query, "limit ?") {
		t.Fatalf("exact count query must not cap matching rows with a limit: %s", query)
	}
	if !strings.Contains(query, "count(*)") || !strings.Contains(query, "from bp_interface_call_log l") {
		t.Fatalf("exact count query must count the call-log relation directly: %s", query)
	}
}

func TestCallLogCountQueryDoesNotIncludeCursorPredicate(t *testing.T) {
	params := CallLogListParams{Cursor: "cursor-token", PageSize: 20}
	where, _ := callLogWhere(params, true, 0)
	query := strings.ToLower(callLogCountQuery(params, where))
	if strings.Contains(query, "l.created_at<?") || strings.Contains(query, "l.id<?") {
		t.Fatalf("count query must describe the complete filter set, not the cursor tail: %s", query)
	}
}

func TestListCallLogsFetchesLookaheadForTheFirstPage(t *testing.T) {
	contents, err := os.ReadFile(filepath.Join("repository.go"))
	if err != nil {
		t.Fatalf("read call-log repository: %v", err)
	}
	source := string(contents)
	start := strings.Index(source, "func (r *Repository) ListCallLogs")
	end := strings.Index(source, "func (r *Repository) ExportCallLogBatch")
	if start < 0 || end <= start {
		t.Fatalf("cannot isolate ListCallLogs implementation")
	}
	function := source[start:end]
	if !strings.Contains(function, "fetchSize := size + 1") {
		t.Fatalf("ListCallLogs must fetch one lookahead row so the first page can emit nextCursor: %s", function)
	}
	if strings.Contains(function, "fetchSize := size\n\tif keyset") {
		t.Fatalf("lookahead must not be limited to already-keyset requests: %s", function)
	}
}

func TestNormalizeCallLogPageKeepsSupportedPageSize(t *testing.T) {
	page, size := normalizePage(1, 200)
	if page != 1 || size != 200 {
		t.Fatalf("normalizePage(1, 200) = %d/%d, want 1/200", page, size)
	}
}

func TestCallLogListQueryPagesNarrowIDsBeforeLoadingWideRows(t *testing.T) {
	params := CallLogListParams{InterfaceID: 12, StartTime: "2026-08-01 00:00:00"}
	where, _ := callLogWhere(params, true, 0)
	query := strings.ToLower(callLogListQuery(params, where, false))

	for _, fragment := range []string{
		"from (",
		"select l.id",
		"from bp_interface_call_log l",
		"order by l.created_at desc,l.id desc limit ? offset ?",
		") filtered",
		"inner join bp_interface_call_log l on l.id=filtered.id",
		"left join bp_interface_api a on a.id = l.interface_id",
		"left join bp_user u on u.id = l.user_id",
		"order by l.created_at desc,l.id desc",
	} {
		if !strings.Contains(query, fragment) {
			t.Fatalf("call-log list query lost %q: %s", fragment, query)
		}
	}
	filterEnd := strings.Index(query, ") filtered")
	filterStart := strings.Index(query, "from (")
	if filterStart < 0 || filterEnd < filterStart {
		t.Fatalf("call-log list query lost filtered ID subquery: %s", query)
	}
	filterQuery := query[filterStart:filterEnd]
	for _, forbidden := range []string{"bp_interface_api", "bp_user", "request_params"} {
		if strings.Contains(filterQuery, forbidden) {
			t.Fatalf("unfiltered ID page unnecessarily loads %q: %s", forbidden, filterQuery)
		}
	}
}

func TestCallLogExportQueryUsesNarrowProjectionAndLateMaterialization(t *testing.T) {
	params := CallLogListParams{InterfaceID: 12, StartTime: "2026-08-01 00:00:00"}
	where, _ := callLogWhere(params, true, 0)
	query := strings.ToLower(callLogExportQuery(params, where))

	for _, fragment := range []string{
		"select l.id",
		"order by l.created_at desc,l.id desc limit ?",
		") filtered",
		"inner join bp_interface_call_log l on l.id=filtered.id",
		"coalesce(l.request_params,'')",
	} {
		if !strings.Contains(query, fragment) {
			t.Fatalf("call-log export query lost %q: %s", fragment, query)
		}
	}
	for _, forbidden := range []string{
		"billing_snapshot",
		"request_value",
		"response_saved_size",
		"latitude",
		"longitude",
		"upstream_url",
	} {
		if strings.Contains(query, forbidden) {
			t.Fatalf("call-log export still loads unused column %q: %s", forbidden, query)
		}
	}
}

func TestListCallLogsRunsCountAndPageQueriesConcurrently(t *testing.T) {
	path := filepath.Join("repository.go")
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read call-log repository: %v", err)
	}
	source := string(contents)
	start := strings.Index(source, "func (r *Repository) ListCallLogs")
	end := strings.Index(source, "func (r *Repository) ExportCallLogBatch")
	if start < 0 || end <= start {
		t.Fatalf("cannot isolate ListCallLogs implementation")
	}
	function := source[start:end]
	if !strings.Contains(function, "dbx.RunParallelContext(ctx,") {
		t.Fatalf("ListCallLogs must run count and page queries concurrently: %s", function)
	}
	if !strings.Contains(function, "callLogCountQuery") ||
		!strings.Contains(function, "callLogListQuery(params, where, keyset)") {
		t.Fatalf("ListCallLogs lost one of its independent queries: %s", function)
	}
}

func TestListCallLogsKeepsCursorOutOfExactTotal(t *testing.T) {
	contents, err := os.ReadFile(filepath.Join("repository.go"))
	if err != nil {
		t.Fatalf("read call-log repository: %v", err)
	}
	source := string(contents)
	start := strings.Index(source, "func (r *Repository) ListCallLogs")
	end := strings.Index(source, "func (r *Repository) ExportCallLogBatch")
	if start < 0 || end <= start {
		t.Fatalf("cannot isolate ListCallLogs implementation")
	}
	function := source[start:end]
	for _, fragment := range []string{
		"baseWhere, baseArgs := callLogWhere",
		"callLogCountQuery(params, baseWhere)",
		"callLogListQuery(params, where, keyset)",
	} {
		if !strings.Contains(function, fragment) {
			t.Fatalf("ListCallLogs lost exact-total cursor separation %q: %s", fragment, function)
		}
	}
	if strings.Contains(function, "size*pagination.MaxPage") || strings.Contains(function, "size * pagination.MaxPage") {
		t.Fatalf("ListCallLogs still derives an implicit count cap from page size: %s", function)
	}
}

func TestCallLogExportBatchCursorContract(t *testing.T) {
	token := encodeCallLogCursor(callLogCursor{CreatedAt: "2026-08-23 10:00:00", ID: 7})
	cursor, err := decodeCallLogCursor(token)
	if err != nil || cursor.ID != 7 {
		t.Fatalf("export cursor contract broken: %+v %v", cursor, err)
	}
}

func TestCallLogWhereUsesEqualityForTraceIPAndAPICode(t *testing.T) {
	params := CallLogListParams{TraceID: "abc123", ClientIP: "1.2.3.4", APICode: "weather.v1"}
	where, args := callLogWhere(params, true, 0)
	for _, fragment := range []string{"l.trace_id=?", "l.client_ip=?", "l.api_code=?"} {
		if !strings.Contains(where, fragment) {
			t.Fatalf("call-log where lost equality fragment %q: %s", fragment, where)
		}
	}
	if strings.Contains(where, "like") {
		t.Fatalf("exact filters must not use LIKE: %s", where)
	}
	if len(args) != 3 {
		t.Fatalf("expected 3 args, got %d: %v", len(args), args)
	}
}

func TestCallLogExactFiltersDoNotRequireFuzzyWindow(t *testing.T) {
	params := CallLogListParams{TraceID: "abc123", ClientIP: "1.2.3.4", APICode: "weather.v1"}
	if callLogFilterNeedsWindow(params) {
		t.Fatalf("exact index-usable filters must not force the 31-day fuzzy window")
	}
}

func TestCallLogInterfaceFailureIndexSupportsFilterAndNewestFirstPage(t *testing.T) {
	migrationPath := filepath.Join("..", "..", "..", "migrations", "000081_call_log_list_index.up.sql")
	contents, err := os.ReadFile(migrationPath)
	if err != nil {
		t.Fatalf("read call-log list index migration: %v", err)
	}
	ddl := strings.ToLower(string(contents))
	compactDDL := strings.Join(strings.Fields(ddl), "")
	for _, fragment := range []string{
		"idx_bp_call_log_interface_success_created_id",
		"(`interface_id`,`success`,`created_at`,`id`)",
	} {
		if !strings.Contains(compactDDL, fragment) {
			t.Fatalf("call-log list index migration lost %q: %s", fragment, ddl)
		}
	}
}

func TestCallLogQueriesCarryPartitionKeyThroughPageJoin(t *testing.T) {
	for _, query := range []string{callLogListQuery(CallLogListParams{}, "where 1=1", true), callLogExportQuery(CallLogListParams{}, "where 1=1")} {
		if !strings.Contains(query, "select l.id,l.created_at") || !strings.Contains(query, "l.created_at=filtered.created_at") {
			t.Fatalf("paged row lookup lost the partition key: %s", query)
		}
	}
}
