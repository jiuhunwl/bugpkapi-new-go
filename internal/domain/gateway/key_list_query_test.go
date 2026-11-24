package gateway

import (
	"os"
	"strings"
	"testing"
)

func TestKeyListCountQuerySkipsUserJoinWithoutKeywordAndCapsCount(t *testing.T) {
	params := KeyListParams{KeyType: "INTERFACE", Page: 1, PageSize: 20}
	where, _ := keyListWhere(1, true, params)
	query := strings.ToLower(keyListCountQuery(params, where))

	for _, fragment := range []string{
		"select count(*)",
		"from (",
		"select k.id",
		"from bp_api_key k",
		"limit ?",
		") counted",
	} {
		if !strings.Contains(query, fragment) {
			t.Fatalf("key count query lost %q: %s", fragment, query)
		}
	}
	if strings.Contains(query, "bp_user") {
		t.Fatalf("key count query unnecessarily joins users: %s", query)
	}
}

func TestKeyListCountQueryKeepsUserLookupForKeyword(t *testing.T) {
	params := KeyListParams{Keyword: "alice", Page: 1, PageSize: 20}
	where, _ := keyListWhere(1, true, params)
	query := strings.ToLower(keyListCountQuery(params, where))

	for _, fragment := range []string{
		"inner join bp_user u on u.id = k.user_id",
		"u.username like ?",
	} {
		if !strings.Contains(query, fragment) {
			t.Fatalf("keyword key count query lost %q: %s", fragment, query)
		}
	}
}

func TestKeyListQueryPagesIDsBeforeLoadingSecretCiphertext(t *testing.T) {
	params := KeyListParams{Status: intPointer(1), Page: 1, PageSize: 20}
	where, _ := keyListWhere(1, false, params)
	query := strings.ToLower(keyListQuery(params, where))

	for _, fragment := range []string{
		"from (",
		"select k.id",
		"from bp_api_key k",
		"order by k.id desc",
		"limit ? offset ?",
		") filtered",
		"inner join bp_api_key k on k.id=filtered.id",
		"inner join bp_user u on u.id = k.user_id",
		"coalesce(k.secret_ciphertext, '')",
	} {
		if !strings.Contains(query, fragment) {
			t.Fatalf("key list query lost %q: %s", fragment, query)
		}
	}
	filterStart := strings.Index(query, "from (")
	filterEnd := strings.Index(query, ") filtered")
	if filterStart < 0 || filterEnd < filterStart {
		t.Fatalf("key list query lost filtered ID subquery: %s", query)
	}
	filterQuery := query[filterStart:filterEnd]
	for _, forbidden := range []string{"secret_ciphertext", "bp_user"} {
		if strings.Contains(filterQuery, forbidden) {
			t.Fatalf("key ID page unnecessarily loads %q: %s", forbidden, filterQuery)
		}
	}
}

func TestListKeysRunsCountAndPageQueriesConcurrently(t *testing.T) {
	contents, err := os.ReadFile("repository.go")
	if err != nil {
		t.Fatalf("read gateway repository: %v", err)
	}
	source := string(contents)
	start := strings.Index(source, "func (r *Repository) ListKeys")
	end := strings.Index(source, "func keyListWhere")
	if start < 0 || end <= start {
		t.Fatalf("cannot isolate ListKeys implementation")
	}
	function := source[start:end]
	if !strings.Contains(function, "dbx.RunParallel(") {
		t.Fatalf("ListKeys must run count and page queries concurrently: %s", function)
	}
	if !strings.Contains(function, "QueryRowContext(ctx, keyListCountQuery") ||
		!strings.Contains(function, "QueryContext(ctx, keyListQuery") {
		t.Fatalf("ListKeys lost one of its independent queries: %s", function)
	}
}

func intPointer(value int) *int {
	return &value
}
