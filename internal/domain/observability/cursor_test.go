package observability

import (
	"strings"
	"testing"
)

func TestEncodeDecodeCallLogCursorRoundTrip(t *testing.T) {
	original := callLogCursor{CreatedAt: "2026-08-23 10:00:00", ID: 42}
	token := encodeCallLogCursor(original)
	decoded, err := decodeCallLogCursor(token)
	if err != nil {
		t.Fatalf("decodeCallLogCursor returned error: %v", err)
	}
	if decoded != original {
		t.Fatalf("round trip mismatch: %+v != %+v", decoded, original)
	}
}

func TestDecodeCallLogCursorEmptyTokenMeansFirstPage(t *testing.T) {
	cursor, err := decodeCallLogCursor("")
	if err != nil || cursor.ID != 0 {
		t.Fatalf("empty token must decode to zero cursor, got %+v err %v", cursor, err)
	}
}

func TestDecodeCallLogCursorRejectsGarbage(t *testing.T) {
	for _, token := range []string{"!!!not-base64!!!", "aGkyfDIwMjYtMDgtMjMgMTA6MDA6MDB8NDI", "MXxiYWR8LTE=", "MXxiYWR8MWIyYw==", "MnwyMDI2LTA4LTIzIDEwOjAwOjAwfDQy"} {
		if _, err := decodeCallLogCursor(token); err == nil {
			t.Fatalf("expected error for cursor token %q", token)
		}
	}
}

func TestCallLogListQueryKeysetHasNoOffset(t *testing.T) {
	params := CallLogListParams{PageSize: 20, Cursor: encodeCallLogCursor(callLogCursor{CreatedAt: "2026-08-23 10:00:00", ID: 42})}
	where, args := callLogWhere(params, true, 0)
	if _, err := decodeCallLogCursor(params.Cursor); err != nil {
		t.Fatalf("decode cursor: %v", err)
	}
	where += " and (l.created_at<? or (l.created_at=? and l.id<?))"
	if len(args) != 0 {
		t.Fatalf("unexpected base args: %v", args)
	}
	keysetQuery := callLogListQuery(params, where, true)
	if strings.Contains(strings.ToLower(keysetQuery), "offset") {
		t.Fatalf("keyset query must not use offset: %s", keysetQuery)
	}
	pageQuery := callLogListQuery(params, where, false)
	if !strings.Contains(strings.ToLower(pageQuery), "limit ? offset ?") {
		t.Fatalf("page query must keep offset: %s", pageQuery)
	}
}
