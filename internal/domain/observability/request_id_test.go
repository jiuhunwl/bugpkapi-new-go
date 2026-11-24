package observability

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestUserCallLogExposesFeedbackRequestID(t *testing.T) {
	item := toUserCallLogItem(CallLogItem{ID: "7", TraceID: "gw_feedback_123"})
	encoded, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err = json.Unmarshal(encoded, &body); err != nil {
		t.Fatal(err)
	}
	if body["traceId"] != "gw_feedback_123" {
		t.Fatalf("user cannot quote request ID for feedback: %s", encoded)
	}
}

func TestUserRequestIDFilterRetainsOwnership(t *testing.T) {
	where, args := callLogWhere(CallLogListParams{TraceID: "other-user-request", UserID: 999, UserVisibleOnly: true}, false, 42)
	if !strings.Contains(where, "l.user_id=?") || !strings.Contains(where, "l.user_visible=1") || !strings.Contains(where, "l.trace_id=?") {
		t.Fatalf("missing ownership or request filter: %s", where)
	}
	if len(args) != 2 || args[0] != int64(42) || args[1] != "other-user-request" {
		t.Fatalf("unexpected query args: %#v", args)
	}
}
