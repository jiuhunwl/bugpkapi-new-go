package ticket

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTicketStatisticsMigrationIndexesBothTimeWindows(t *testing.T) {
	path := filepath.Join("..", "..", "..", "migrations", "000082_ticket_statistics_indexes.up.sql")
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read ticket statistics index migration: %v", err)
	}
	ddl := strings.Join(strings.Fields(strings.ToLower(string(contents))), "")
	for _, fragment := range []string{
		"`idx_bp_ticket_created_at`(`created_at`)",
		"`idx_bp_ticket_resolved_at`(`resolved_at`)",
	} {
		if !strings.Contains(ddl, fragment) {
			t.Fatalf("ticket statistics migration lost %q: %s", fragment, ddl)
		}
	}
}

func TestTicketListNeedsCountOnlyWhenFiltersAreActive(t *testing.T) {
	if ticketListNeedsCount(ListParams{}) {
		t.Fatal("unfiltered ticket list should reuse Summary.All instead of issuing a duplicate count")
	}
	for _, params := range []ListParams{
		{Category: CategoryAPI},
		{Keyword: "timeout"},
		{Priority: PriorityHigh},
		{Status: StatusOpen},
		{Unread: true},
	} {
		if !ticketListNeedsCount(params) {
			t.Fatalf("filtered ticket list skipped its filtered total: %+v", params)
		}
	}
}

func TestTicketCountQuerySkipsUserJoinWithoutKeywordAndCapsCount(t *testing.T) {
	params := ListParams{Status: StatusOpen}
	where, _ := ticketWhere(1, true, params)
	query := strings.ToLower(ticketCountQuery(params, where))

	for _, fragment := range []string{
		"select count(*)",
		"from (",
		"select t.id",
		"from bp_ticket t",
		"limit ?",
		") counted",
	} {
		if !strings.Contains(query, fragment) {
			t.Fatalf("ticket count query lost %q: %s", fragment, query)
		}
	}
	if strings.Contains(query, "bp_user") {
		t.Fatalf("ticket count query unnecessarily joins users: %s", query)
	}
}

func TestTicketCountQueryKeepsUserLookupForKeyword(t *testing.T) {
	params := ListParams{Keyword: "alice"}
	where, _ := ticketWhere(1, true, params)
	query := strings.ToLower(ticketCountQuery(params, where))

	for _, fragment := range []string{
		"inner join bp_user u on u.id=t.user_id",
		"u.username like ?",
		"u.display_name like ?",
	} {
		if !strings.Contains(query, fragment) {
			t.Fatalf("keyword ticket count query lost %q: %s", fragment, query)
		}
	}
}

func TestTicketListQueryPagesIDsBeforeLoadingWideRows(t *testing.T) {
	params := ListParams{Status: StatusOpen}
	where, _ := ticketWhere(1, true, params)
	query := strings.ToLower(ticketListQuery(true, params, where))

	for _, fragment := range []string{
		"inner join (",
		"select t.id",
		"from bp_ticket t",
		"limit ? offset ?",
		") filtered on filtered.id=t.id",
		"inner join bp_user u on u.id=t.user_id",
		"left join bp_user a on a.id=t.assigned_to",
	} {
		if !strings.Contains(query, fragment) {
			t.Fatalf("ticket list query lost %q: %s", fragment, query)
		}
	}
	filterStart := strings.Index(query, "inner join (")
	filterEnd := strings.Index(query, ") filtered")
	if filterStart < 0 || filterEnd < filterStart {
		t.Fatalf("ticket list query lost filtered ID subquery: %s", query)
	}
	filterQuery := query[filterStart:filterEnd]
	for _, forbidden := range []string{"last_message_preview", "first_response_due_at", "bp_user"} {
		if strings.Contains(filterQuery, forbidden) {
			t.Fatalf("ticket ID page unnecessarily loads %q: %s", forbidden, filterQuery)
		}
	}
}
