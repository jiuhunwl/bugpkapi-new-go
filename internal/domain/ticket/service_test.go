package ticket

import (
	"strings"
	"testing"
)

func TestNormalizeMarkReadRequestRequiresCanonicalSnapshot(t *testing.T) {
	got, err := normalizeMarkReadRequest(MarkReadRequest{
		LastMessageAt: " 2026-08-15 10:01:02 ",
		MessageCount:  4,
	})
	if err != nil || got.LastMessageAt != "2026-08-15 10:01:02" || got.MessageCount != 4 {
		t.Fatalf("normalized snapshot = %#v, %v", got, err)
	}
	for _, value := range []string{"", "2026-08-15", "2026-02-30 10:01:02", "2026-08-15T10:01:02Z"} {
		if _, err := normalizeMarkReadRequest(MarkReadRequest{LastMessageAt: value, MessageCount: 4}); err == nil {
			t.Fatalf("snapshot %q unexpectedly accepted", value)
		}
	}
	if _, err := normalizeMarkReadRequest(MarkReadRequest{LastMessageAt: "2026-08-15 10:01:02"}); err == nil {
		t.Fatal("zero message count unexpectedly accepted")
	}
}

func TestNormalizeCreateRequest(t *testing.T) {
	got, err := normalizeCreateRequest(CreateRequest{
		Subject: "  API signature failure  ",
		Content: "  request keeps failing  ",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Subject != "API signature failure" || got.Content != "request keeps failing" {
		t.Fatalf("normalized text = %#v", got)
	}
	if got.Category != CategoryGeneral || got.Priority != PriorityNormal {
		t.Fatalf("defaults = category %q priority %q", got.Category, got.Priority)
	}

	for name, request := range map[string]CreateRequest{
		"empty subject":    {Subject: "", Content: "details"},
		"long subject":     {Subject: strings.Repeat("题", maxTicketSubjectRunes+1), Content: "details"},
		"invalid category": {Subject: "subject", Content: "details", Category: "SECURITY"},
		"invalid priority": {Subject: "subject", Content: "details", Priority: "BLOCKER"},
		"empty content":    {Subject: "subject", Content: "  "},
		"long content":     {Subject: "subject", Content: strings.Repeat("内", maxTicketContentRunes+1)},
		"nul content":      {Subject: "subject", Content: "hello\x00world"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := normalizeCreateRequest(request); err == nil {
				t.Fatalf("request unexpectedly accepted: %#v", request)
			}
		})
	}
}

func TestNormalizeListParams(t *testing.T) {
	got, err := normalizeListParams(ListParams{
		Category: " api ",
		Keyword:  "  signature ",
		Priority: " high ",
		Status:   " processing ",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Category != CategoryAPI || got.Keyword != "signature" || got.Priority != PriorityHigh || got.Status != StatusProcessing {
		t.Fatalf("normalized params = %#v", got)
	}

	for name, params := range map[string]ListParams{
		"long keyword":     {Keyword: strings.Repeat("k", maxTicketKeywordRunes+1)},
		"invalid category": {Category: "UNKNOWN"},
		"invalid priority": {Priority: "BLOCKER"},
		"invalid status":   {Status: "DELETED"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := normalizeListParams(params); err == nil {
				t.Fatalf("params unexpectedly accepted: %#v", params)
			}
		})
	}
}

func TestNormalizeUpdateRequest(t *testing.T) {
	status := " processing "
	assignedTo := " 42 "
	got, err := normalizeUpdateRequest(UpdateRequest{Status: &status, AssignedTo: &assignedTo})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusProcessing || !got.HasAssignment || got.AssignedTo != 42 {
		t.Fatalf("normalized update = %#v", got)
	}

	clearAssignment := ""
	got, err = normalizeUpdateRequest(UpdateRequest{AssignedTo: &clearAssignment})
	if err != nil || !got.HasAssignment || got.AssignedTo != 0 {
		t.Fatalf("clear assignment = %#v, %v", got, err)
	}

	invalidStatus := "DELETED"
	invalidAssignee := "not-an-id"
	for name, request := range map[string]UpdateRequest{
		"empty update":     {},
		"invalid status":   {Status: &invalidStatus},
		"invalid assignee": {AssignedTo: &invalidAssignee},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := normalizeUpdateRequest(request); err == nil {
				t.Fatalf("update unexpectedly accepted: %#v", request)
			}
		})
	}
}

func TestNextStatusAfterReply(t *testing.T) {
	tests := []struct {
		current string
		staff   bool
		want    string
	}{
		{current: StatusOpen, staff: true, want: StatusWaitingUser},
		{current: StatusProcessing, staff: true, want: StatusWaitingUser},
		{current: StatusResolved, staff: false, want: StatusOpen},
		{current: StatusWaitingUser, staff: false, want: StatusOpen},
		{current: StatusClosed, staff: true, want: StatusClosed},
		{current: StatusClosed, staff: false, want: StatusClosed},
	}
	for _, test := range tests {
		if got := nextStatusAfterReply(test.current, test.staff); got != test.want {
			t.Fatalf("nextStatusAfterReply(%q, %v) = %q, want %q", test.current, test.staff, got, test.want)
		}
	}
}

func TestMessagePreviewIsWhitespaceNormalizedAndBounded(t *testing.T) {
	got := messagePreview("  first\n\tsecond   third  ")
	if got != "first second third" {
		t.Fatalf("preview = %q", got)
	}
	got = messagePreview(strings.Repeat("界", 130))
	if !strings.HasSuffix(got, "…") || len([]rune(strings.TrimSuffix(got, "…"))) != 120 {
		t.Fatalf("bounded preview = %q", got)
	}
}

func TestBoundedQueryInt(t *testing.T) {
	if got, err := boundedQueryInt("", 20, 1, 100); err != nil || got != 20 {
		t.Fatalf("fallback = %d, %v", got, err)
	}
	if got, err := boundedQueryInt(" 50 ", 20, 1, 100); err != nil || got != 50 {
		t.Fatalf("parsed = %d, %v", got, err)
	}
	for _, value := range []string{"0", "101", "invalid"} {
		if _, err := boundedQueryInt(value, 20, 1, 100); err == nil {
			t.Fatalf("value %q unexpectedly accepted", value)
		}
	}
}

func TestBinaryQueryFlag(t *testing.T) {
	for _, value := range []string{"", "0", " 0 "} {
		if got, err := binaryQueryFlag(value); err != nil || got {
			t.Fatalf("binaryQueryFlag(%q) = %v, %v; want false", value, got, err)
		}
	}
	for _, value := range []string{"1", " 1 "} {
		if got, err := binaryQueryFlag(value); err != nil || !got {
			t.Fatalf("binaryQueryFlag(%q) = %v, %v; want true", value, got, err)
		}
	}
	for _, value := range []string{"2", "true", "invalid"} {
		if _, err := binaryQueryFlag(value); err == nil {
			t.Fatalf("binaryQueryFlag(%q) unexpectedly accepted", value)
		}
	}
}

func TestTicketWhereUnreadUsesViewerColumn(t *testing.T) {
	userWhere, _ := ticketWhere(42, false, ListParams{Unread: true})
	if !strings.Contains(userWhere, "t.unread_user>0") {
		t.Fatalf("user unread filter = %q", userWhere)
	}
	adminWhere, _ := ticketWhere(42, true, ListParams{Unread: true})
	if !strings.Contains(adminWhere, "t.unread_admin>0") {
		t.Fatalf("admin unread filter = %q", adminWhere)
	}
}
