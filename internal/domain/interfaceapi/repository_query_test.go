package interfaceapi

import (
	"os"
	"strings"
	"testing"
)

func TestRecentCallCountsQueryUsesBoundedActiveUserRollups(t *testing.T) {
	query := strings.ToLower(recentCallCountsQuery("?,?,?"))
	for _, fragment := range []string{
		"select m.interface_id,cast(coalesce(sum(m.call_count),0) as unsigned)",
		"from bp_call_api_metric_minute m force index (idx_bp_call_api_metric_interface_time)",
		"inner join bp_user u on u.id=m.user_id",
		"m.interface_id in (?,?,?)",
		"m.bucket_start >= date_sub(now(), interval ? day)",
		"case when u.status = 0",
		"group by m.interface_id",
	} {
		if !strings.Contains(query, fragment) {
			t.Fatalf("recent call-count query lost %q: %s", fragment, query)
		}
	}
	if strings.Contains(query, "bp_interface_call_log") {
		t.Fatalf("recent call-count query still scans raw logs: %s", query)
	}
}

func TestInterfaceTrend7dQueryUsesBoundedActiveUserRollups(t *testing.T) {
	query := strings.ToLower(interfaceTrend7dQuery())
	for _, fragment := range []string{
		"select date_format(m.bucket_start,'%y-%m-%d')",
		"cast(coalesce(sum(m.call_count),0) as unsigned)",
		"from bp_call_api_metric_minute m force index (idx_bp_call_api_metric_interface_time)",
		"inner join bp_user u on u.id=m.user_id",
		"m.interface_id=?",
		"m.bucket_start>=?",
		"m.bucket_start<?",
		"case when u.status = 0",
		"group by date_format(m.bucket_start,'%y-%m-%d')",
	} {
		if !strings.Contains(query, fragment) {
			t.Fatalf("interface trend query lost %q: %s", fragment, query)
		}
	}
	if strings.Contains(query, "bp_interface_call_log") {
		t.Fatalf("interface trend query still scans raw logs: %s", query)
	}
}

func TestInterfaceCountQueryStopsAtReachablePaginationRange(t *testing.T) {
	status := 1
	where, _ := listWhere(ListParams{Status: &status}, true)
	query := strings.ToLower(interfaceCountQuery(where))

	for _, fragment := range []string{
		"select count(*)",
		"from (",
		"select a.id",
		"from bp_interface_api a",
		"limit ?",
		") counted",
	} {
		if !strings.Contains(query, fragment) {
			t.Fatalf("interface count query lost %q: %s", fragment, query)
		}
	}
}

func TestInterfaceListQueryPagesIDsBeforeLoadingDocumentationJSON(t *testing.T) {
	where, _ := listWhere(ListParams{Keyword: "weather"}, true)
	query := strings.ToLower(interfaceListQuery(where))

	for _, fragment := range []string{
		"from (",
		"select a.id",
		"from bp_interface_api a",
		"order by a.is_top desc, a.is_featured desc, a.id desc",
		"limit ? offset ?",
		") filtered",
		"inner join bp_interface_api a on a.id=filtered.id",
		"cast(a.upstream_urls as char)",
		"a.doc_response_example",
	} {
		if !strings.Contains(query, fragment) {
			t.Fatalf("interface list query lost %q: %s", fragment, query)
		}
	}

	filterStart := strings.Index(query, "from (")
	filterEnd := strings.Index(query, ") filtered")
	if filterStart < 0 || filterEnd < filterStart {
		t.Fatalf("interface list query lost filtered ID subquery: %s", query)
	}
	filterQuery := query[filterStart:filterEnd]
	for _, forbidden := range []string{"upstream_urls", "doc_request_params", "doc_response_fields", "doc_response_example", "doc_status_codes"} {
		if strings.Contains(filterQuery, forbidden) {
			t.Fatalf("interface ID page unnecessarily loads %q: %s", forbidden, filterQuery)
		}
	}
}

func TestInterfaceListRunsCountAndPageQueriesConcurrently(t *testing.T) {
	contents, err := os.ReadFile("repository.go")
	if err != nil {
		t.Fatalf("read interface repository: %v", err)
	}
	source := string(contents)
	start := strings.Index(source, "func (r *Repository) List(ctx")
	end := strings.Index(source, "func interfaceCountQuery")
	if start < 0 || end <= start {
		t.Fatalf("cannot isolate interface List implementation")
	}
	function := source[start:end]
	if !strings.Contains(function, "dbx.RunParallel(") {
		t.Fatalf("interface List must run count and page queries concurrently: %s", function)
	}
	if !strings.Contains(function, "QueryRowContext(ctx, interfaceCountQuery") ||
		!strings.Contains(function, "QueryContext(ctx, interfaceListQuery") {
		t.Fatalf("interface List lost one of its independent queries: %s", function)
	}
}
