package jsonappend

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAppendTailFieldsPreservesOrderAndReplacesExistingKeys(t *testing.T) {
	got := AppendTailFields(
		[]byte(`{"z":1,"nested":{"b":2,"a":1},"platform":"forged","a":3}`),
		Field{Key: "platform", Value: json.RawMessage(`{"status":"real"}`)},
		Field{Key: "cache", Value: true},
	)
	want := `{"z":1,"nested":{"b":2,"a":1},"a":3,"platform":{"status":"real"},"cache":true}`
	if string(got) != want {
		t.Fatalf("body=%s, want %s", got, want)
	}
	if strings.Count(string(got), `"platform"`) != 1 {
		t.Fatalf("platform key was duplicated: %s", got)
	}
}

func TestAppendTailFieldsSupportsEmptyObjectAndRejectsNonObject(t *testing.T) {
	if got := string(AppendTailFields([]byte(`{}`), Field{Key: "a", Value: 1})); got != `{"a":1}` {
		t.Fatalf("empty object result=%s", got)
	}
	if got := string(AppendTailFields([]byte(`[1,2]`), Field{Key: "a", Value: 1})); got != `[1,2]` {
		t.Fatalf("array was modified: %s", got)
	}
}
