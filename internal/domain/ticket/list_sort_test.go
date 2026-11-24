package ticket

import (
	"strings"
	"testing"
)

func TestTicketOrderByWhitelist(t *testing.T) {
	if got := ticketOrderBy(ListParams{}); !strings.Contains(got, "case t.priority") ||
		!strings.Contains(got, "last_message_at desc") {
		t.Fatalf("default order = %q", got)
	}
	if got := ticketOrderBy(ListParams{SortBy: "created_at", SortOrder: "asc"}); got != "t.created_at asc,t.id desc" {
		t.Fatalf("created_at asc order = %q", got)
	}
	if got := ticketOrderBy(ListParams{SortBy: "sla_due", SortOrder: "desc"}); !strings.Contains(got, "first_response_due_at") {
		t.Fatalf("sla_due order = %q", got)
	}
	if got := ticketOrderBy(ListParams{SortBy: "priority", SortOrder: "desc"}); !strings.Contains(got, "case t.priority") ||
		!strings.Contains(got, "desc") {
		t.Fatalf("priority desc order = %q", got)
	}
}

func TestNormalizeTicketListSortAndFilters(t *testing.T) {
	got, err := normalizeListParams(ListParams{
		AssignedTo:  42,
		CreatedFrom: " 2026-01-01 ",
		CreatedTo:   "2026-01-31",
		Overdue:     true,
		SortBy:      " Priority ",
		SortOrder:   " ASC ",
		UpdatedFrom: "2026-01-15",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.CreatedFrom != "2026-01-01" || got.CreatedTo != "2026-01-31" || got.UpdatedFrom != "2026-01-15" {
		t.Fatalf("normalized dates = %+v", got)
	}
	if got.SortBy != "priority" || got.SortOrder != "asc" || got.AssignedTo != 42 || !got.Overdue {
		t.Fatalf("normalized filters = %+v", got)
	}

	for name, params := range map[string]ListParams{
		"bad created date":   {CreatedFrom: "2026/01/01"},
		"bad updated date":   {UpdatedTo: "yesterday"},
		"reversed created":   {CreatedFrom: "2026-02-01", CreatedTo: "2026-01-01"},
		"reversed updated":   {UpdatedFrom: "2026-02-01", UpdatedTo: "2026-01-01"},
		"unknown sort field": {SortBy: "payload"},
		"unknown direction":  {SortOrder: "sideways"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := normalizeListParams(params); err == nil {
				t.Fatalf("params unexpectedly accepted: %+v", params)
			}
		})
	}
}
