package advertising

import (
	"os"
	"strings"
	"testing"
)

func TestAdvertisementListRunsCountAndPageQueriesConcurrently(t *testing.T) {
	contents, err := os.ReadFile("repository.go")
	if err != nil {
		t.Fatalf("read advertising repository: %v", err)
	}
	source := string(contents)
	start := strings.Index(source, "func (r *Repository) List")
	end := strings.Index(source, "func (r *Repository) ByID")
	if start < 0 || end <= start {
		t.Fatalf("cannot isolate advertisement List implementation")
	}
	function := source[start:end]
	if !strings.Contains(function, "dbx.RunParallel(") {
		t.Fatalf("advertisement List must run count and page queries concurrently: %s", function)
	}
}
