package packagepkg

import (
	"os"
	"strings"
	"testing"
)

func TestWalletTransactionQueriesCapCountAndUseUserTimeOrder(t *testing.T) {
	countQuery := strings.ToLower(walletTransactionCountQuery())
	for _, fragment := range []string{
		"select count(*)",
		"from (",
		"select id",
		"from bp_wallet_transaction",
		"where user_id=?",
		"limit ?",
		") counted",
	} {
		if !strings.Contains(countQuery, fragment) {
			t.Fatalf("wallet count query lost %q: %s", fragment, countQuery)
		}
	}

	listQuery := strings.ToLower(walletTransactionListQuery())
	if !strings.Contains(listQuery, "order by created_at desc,id desc limit ? offset ?") {
		t.Fatalf("wallet list query does not match the user/time index order: %s", listQuery)
	}
}

func TestBillingRecordQueriesCapCountAndUseUserTimeOrder(t *testing.T) {
	where := "where 1=1 and user_id=?"
	countQuery := strings.ToLower(billingRecordCountQuery(where))
	for _, fragment := range []string{
		"select count(*)",
		"from (",
		"select id",
		"from bp_billing_record",
		"where 1=1 and user_id=?",
		"limit ?",
		") counted",
	} {
		if !strings.Contains(countQuery, fragment) {
			t.Fatalf("billing count query lost %q: %s", fragment, countQuery)
		}
	}

	listQuery := strings.ToLower(billingRecordListQuery(where))
	if !strings.Contains(listQuery, "order by created_at desc,id desc limit ? offset ?") {
		t.Fatalf("billing list query does not match the user/time index order: %s", listQuery)
	}
}

func TestPackagePagedReadsRunCountAndPageQueriesConcurrently(t *testing.T) {
	contents, err := os.ReadFile("repository.go")
	if err != nil {
		t.Fatalf("read package repository: %v", err)
	}
	source := string(contents)
	for _, name := range []string{"ListGlobal", "ListPoint", "ListInterface", "WalletTransactions", "BillingRecords"} {
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
