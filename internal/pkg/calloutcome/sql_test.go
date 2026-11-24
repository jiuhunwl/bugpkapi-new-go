package calloutcome

import (
	"strings"
	"testing"
)

func TestSQLClassQualifiesColumns(t *testing.T) {
	qualified := SQLClass("l")
	for _, fragment := range []string{
		"l.outcome_class in (1,2,3,4,5)",
		"l.success=1",
		"coalesce(l.response_status,0)=404",
		"coalesce(l.response_status,0)=429",
		"between 400 and 499",
	} {
		if !strings.Contains(qualified, fragment) {
			t.Fatalf("qualified SQL missing %q: %s", fragment, qualified)
		}
	}
	bare := SQLClass("")
	if strings.Contains(bare, "l.") || !strings.Contains(bare, "outcome_class in") {
		t.Fatalf("bare SQL must not qualify columns: %s", bare)
	}
}

func TestSQLLegacyFailureStaysCompatible(t *testing.T) {
	predicate := SQLLegacyFailure("s")
	for _, fragment := range []string{"s.success=0", "coalesce(s.response_status,0)<>404"} {
		if !strings.Contains(predicate, fragment) {
			t.Fatalf("legacy failure predicate missing %q: %s", fragment, predicate)
		}
	}
	// Since 1.2.54 the predicate reuses the shared class expression to exclude
	// expected-empty business 404 rows (outcome_class=2, HTTP 2xx + code 404):
	// they are normal results and must not inflate failure_count surfaces.
	for _, fragment := range []string{"s.outcome_class in (1,2,3,4,5)", "<>2)"} {
		if !strings.Contains(predicate, fragment) {
			t.Fatalf("legacy failure predicate must exclude expected-empty rows via the shared class expression: %s", predicate)
		}
	}
	bare := SQLLegacyFailure("")
	if strings.Contains(bare, "l.") {
		t.Fatalf("bare predicate must not qualify columns: %s", bare)
	}
}
