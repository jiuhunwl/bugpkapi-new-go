package requestid

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestEnsureReusesOnlyRequestContext(t *testing.T) {
	ctx := Ensure(context.Background())
	if Ensure(ctx) != ctx || FromContext(ctx) == "" {
		t.Fatal("request context lost its ID")
	}
	seen := map[string]bool{FromContext(ctx): true}
	for i := 0; i < 1000; i++ {
		id := FromContext(Ensure(context.Background()))
		if seen[id] || len(id) > 64 {
			t.Fatalf("bad ID %q", id)
		}
		seen[id] = true
	}
}

func TestDecoratePreservesDataAndOverridesUpstreamID(t *testing.T) {
	src := []byte(`{"data":{"id":9007199254740993,"nested":{"b":2,"a":1}},"requestId":"forged","requestId":"duplicate"}`)
	got := Decorate(src, "application/json; charset=utf-8", "gw_real")
	if string(got) != `{"data":{"id":9007199254740993,"nested":{"b":2,"a":1}},"requestId":"gw_real"}` {
		t.Fatalf("response changed: %s", got)
	}
	if strings.Contains(string(src), "gw_real") {
		t.Fatal("mutated cached buffer")
	}
	for _, id := range []string{"gw_first", "gw_second"} {
		body := Decorate(src, "application/problem+json", id)
		var v map[string]json.RawMessage
		if err := json.Unmarshal(body, &v); err != nil {
			t.Fatal(err)
		}
		if string(v["requestId"]) != `"`+id+`"` {
			t.Fatalf("stale cache ID: %s", body)
		}
	}
}

func TestDecorateLeavesNonObjectResponsesByteExact(t *testing.T) {
	for _, tc := range []struct{ body, ct string }{
		{" [1,2] \n", "application/json"}, {" null \n", "application/json"}, {"\"hello\"", "application/json"}, {"{broken", "application/json"}, {`{"ok":true} trailing`, "application/json"}, {`{"ok":true}`, "text/plain"}, {"\x89PNG\x00", "image/png"}, {"", "application/json"},
	} {
		if got := Decorate([]byte(tc.body), tc.ct, "gw_id"); !bytes.Equal(got, []byte(tc.body)) {
			t.Fatalf("changed %q: %q", tc.body, got)
		}
	}
}
