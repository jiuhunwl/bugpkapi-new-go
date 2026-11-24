package rbac

import (
	"os"
	"strings"
	"testing"
)

func TestListRolesRunsCountAndPageQueriesConcurrently(t *testing.T) {
	contents, err := os.ReadFile("repository.go")
	if err != nil {
		t.Fatalf("read rbac repository: %v", err)
	}
	source := string(contents)
	start := strings.Index(source, "func (r *Repository) ListRoles")
	end := strings.Index(source, "func (r *Repository) RoleOptions")
	if start < 0 || end <= start {
		t.Fatalf("cannot isolate ListRoles implementation")
	}
	function := source[start:end]
	if !strings.Contains(function, "dbx.RunParallel(") {
		t.Fatalf("ListRoles must run count and page queries concurrently: %s", function)
	}
}
