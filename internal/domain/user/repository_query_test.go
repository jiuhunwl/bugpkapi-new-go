package user

import (
	"os"
	"strings"
	"testing"
)

func TestUserCountQueryStopsAtReachablePaginationRange(t *testing.T) {
	status := 1
	where, _ := buildUserWhere(UserListParams{Status: &status, RoleID: 2})
	query := strings.ToLower(userCountQuery(where))

	for _, fragment := range []string{
		"select count(*)",
		"from (",
		"select u.id",
		"from bp_user u",
		"limit ?",
		") counted",
		"exists (select 1 from bp_user_role ur where ur.user_id = u.id and ur.role_id = ?)",
	} {
		if !strings.Contains(query, fragment) {
			t.Fatalf("user count query lost %q: %s", fragment, query)
		}
	}
}

func TestUserListQueryPagesIDsBeforeLoadingWideAccountFields(t *testing.T) {
	where, _ := buildUserWhere(UserListParams{Keyword: "alice"})
	query := strings.ToLower(userListQuery(where))

	for _, fragment := range []string{
		"from (",
		"select u.id",
		"from bp_user u",
		"order by u.id desc",
		"limit ? offset ?",
		") filtered",
		"inner join bp_user u on u.id=filtered.id",
		"u.password_hash",
		"cast(u.balance as char)",
	} {
		if !strings.Contains(query, fragment) {
			t.Fatalf("user list query lost %q: %s", fragment, query)
		}
	}

	filterStart := strings.Index(query, "from (")
	filterEnd := strings.Index(query, ") filtered")
	if filterStart < 0 || filterEnd < filterStart {
		t.Fatalf("user list query lost filtered ID subquery: %s", query)
	}
	filterQuery := query[filterStart:filterEnd]
	for _, forbidden := range []string{"password_hash", "balance", "ban_reason", "date_format"} {
		if strings.Contains(filterQuery, forbidden) {
			t.Fatalf("user ID page unnecessarily loads %q: %s", forbidden, filterQuery)
		}
	}
}

func TestListUsersRunsCountAndPageQueriesConcurrently(t *testing.T) {
	contents, err := os.ReadFile("repository.go")
	if err != nil {
		t.Fatalf("read user repository: %v", err)
	}
	source := string(contents)
	start := strings.Index(source, "func (r *Repository) ListUsers")
	end := strings.Index(source, "func userCountQuery")
	if start < 0 || end <= start {
		t.Fatalf("cannot isolate ListUsers implementation")
	}
	function := source[start:end]
	if !strings.Contains(function, "dbx.RunParallel(") {
		t.Fatalf("ListUsers must run count and page queries concurrently: %s", function)
	}
	if !strings.Contains(function, "QueryRowContext(ctx, userCountQuery") ||
		!strings.Contains(function, "QueryContext(ctx, userListQuery") {
		t.Fatalf("ListUsers lost one of its independent queries: %s", function)
	}
}
