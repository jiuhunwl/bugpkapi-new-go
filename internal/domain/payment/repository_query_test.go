package payment

import (
	"os"
	"strings"
	"testing"
)

func TestPaymentOrderCountQuerySkipsUserJoinWithoutKeywordAndCapsCount(t *testing.T) {
	params := ListParams{Status: "PAID", Page: 1, PageSize: 20}
	where, _ := paymentOrderWhere(params, 0, true)
	query := strings.ToLower(paymentOrderCountQuery(params, where))

	for _, fragment := range []string{
		"select count(*)",
		"from (",
		"select o.id",
		"from bp_payment_order o",
		"limit ?",
		") counted",
	} {
		if !strings.Contains(query, fragment) {
			t.Fatalf("payment-order count query lost %q: %s", fragment, query)
		}
	}
	if strings.Contains(query, "bp_user") {
		t.Fatalf("payment-order count query unnecessarily joins users: %s", query)
	}
}

func TestPaymentOrderCountQueryKeepsUserLookupForKeyword(t *testing.T) {
	params := ListParams{Keyword: "alice", Page: 1, PageSize: 20}
	where, _ := paymentOrderWhere(params, 0, true)
	query := strings.ToLower(paymentOrderCountQuery(params, where))

	for _, fragment := range []string{
		"left join bp_user u on u.id=o.user_id",
		"u.username like ?",
		"u.display_name like ?",
	} {
		if !strings.Contains(query, fragment) {
			t.Fatalf("payment-order keyword count query lost %q: %s", fragment, query)
		}
	}
}

func TestPaymentOrderListQueryPagesIDsBeforeLoadingWideRows(t *testing.T) {
	params := ListParams{Status: "PAID", Page: 1, PageSize: 20}
	where, _ := paymentOrderWhere(params, 0, true)
	query := strings.ToLower(paymentOrderListQuery(params, where))

	for _, fragment := range []string{
		"from (",
		"select o.id",
		"from bp_payment_order o",
		"order by o.id desc limit ? offset ?",
		") filtered",
		"inner join bp_payment_order o on o.id=filtered.id",
		"left join bp_user u on u.id=o.user_id",
	} {
		if !strings.Contains(query, fragment) {
			t.Fatalf("payment-order list query lost %q: %s", fragment, query)
		}
	}
	filterStart := strings.Index(query, "from (")
	filterEnd := strings.Index(query, ") filtered")
	if filterStart < 0 || filterEnd < filterStart {
		t.Fatalf("payment-order list query lost filtered ID subquery: %s", query)
	}
	filterQuery := query[filterStart:filterEnd]
	for _, forbidden := range []string{"pay_form", "pay_url", "qr_code", "bp_user"} {
		if strings.Contains(filterQuery, forbidden) {
			t.Fatalf("payment-order ID page unnecessarily loads %q: %s", forbidden, filterQuery)
		}
	}
}

func TestPaymentPagedReadsRunCountAndPageQueriesConcurrently(t *testing.T) {
	contents, err := os.ReadFile("repository.go")
	if err != nil {
		t.Fatalf("read payment repository: %v", err)
	}
	source := string(contents)
	for _, name := range []string{"ListPresets", "ListOrders"} {
		start := strings.Index(source, "func (r *Repository) "+name)
		if start < 0 {
			t.Fatalf("cannot find %s implementation", name)
		}
		end := strings.Index(source[start+1:], "func ")
		if end >= 0 {
			end += start + 1
		} else {
			end = len(source)
		}
		function := source[start:end]
		if !strings.Contains(function, "dbx.RunParallel(") {
			t.Fatalf("%s must run count and page queries concurrently: %s", name, function)
		}
	}
}
