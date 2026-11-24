package jsoncleaning

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRenameFieldsRecursesThroughObjectsAndArrays(t *testing.T) {
	result, err := RenameFields([]byte(`{"payload":{"media_link":"video.mp4","items":[{"thumb_link":"cover.jpg"}]}}`), []FieldMapping{
		{Source: "payload", Target: "data"},
		{Source: "media_link", Target: "url"},
		{Source: "thumb_link", Target: "cover"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed || !result.IsJSON {
		t.Fatalf("unexpected result metadata: %+v", result)
	}
	var body map[string]any
	if err := json.Unmarshal(result.Body, &body); err != nil {
		t.Fatal(err)
	}
	data := body["data"].(map[string]any)
	if data["url"] != "video.mp4" {
		t.Fatalf("mapped data = %#v", data)
	}
	item := data["items"].([]any)[0].(map[string]any)
	if item["cover"] != "cover.jpg" {
		t.Fatalf("mapped item = %#v", item)
	}
}

func TestRenameFieldsPreservesUpstreamKeyOrder(t *testing.T) {
	result, err := RenameFields(
		[]byte(`{"trace":"first","payload":{"z":1,"media_link":"video.mp4","a":2},"requestId":"last"}`),
		[]FieldMapping{{Source: "payload", Target: "data"}, {Source: "media_link", Target: "url"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"trace":"first","data":{"z":1,"url":"video.mp4","a":2},"requestId":"last"}`
	if string(result.Body) != want {
		t.Fatalf("body=%s, want %s", result.Body, want)
	}
}

func TestRenameFieldsPreservesCanonicalTargetOnConflict(t *testing.T) {
	result, err := RenameFields([]byte(`{"video_url":"alias.mp4","url":"canonical.mp4"}`), []FieldMapping{{Source: "video_url", Target: "url"}})
	if err != nil {
		t.Fatal(err)
	}
	if string(result.Body) != `{"url":"canonical.mp4"}` {
		t.Fatalf("body = %s", result.Body)
	}
}

func TestRenameFieldsPassesThroughNonJSONAndRejectsMalformedJSON(t *testing.T) {
	raw := []byte("plain text")
	result, err := RenameFields(raw, []FieldMapping{{Source: "a", Target: "b"}})
	if err != nil || string(result.Body) != string(raw) || result.IsJSON {
		t.Fatalf("non-JSON result=%+v err=%v", result, err)
	}
	if _, err := RenameFields([]byte(`{"a":`), []FieldMapping{{Source: "a", Target: "b"}}); err == nil {
		t.Fatal("malformed JSON was accepted")
	}
}

func TestNormalizeMappingsRejectsAmbiguousRules(t *testing.T) {
	for _, mappings := range [][]FieldMapping{
		{{Source: "a", Target: "b"}, {Source: "a", Target: "c"}},
		{{Source: "a", Target: "c"}, {Source: "b", Target: "c"}},
		{{Source: "a", Target: "b"}, {Source: "b", Target: "c"}},
	} {
		if _, err := NormalizeMappings(mappings); err == nil {
			t.Fatalf("ambiguous mappings accepted: %#v", mappings)
		}
	}
	if _, err := NormalizeMappings([]FieldMapping{{Source: strings.Repeat("a", MaxFieldNameRunes+1), Target: "url"}}); err == nil {
		t.Fatal("oversized source was accepted")
	}
}

func TestRenameFieldsMapsExplicitJSONPathsAndWildcards(t *testing.T) {
	raw := []byte(`{"payload":{"author":{"nickname":"Ada"},"videos":[{"play":"a.mp4"},{"play":"b.mp4"}]}}`)
	result, err := RenameFields(raw, []FieldMapping{
		{Source: "$.payload.author.nickname", Target: "data.author.name"},
		{Source: "payload.videos[].play", Target: "data.video_backup[].url"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(result.Body, &body); err != nil {
		t.Fatal(err)
	}
	data := body["data"].(map[string]any)
	author := data["author"].(map[string]any)
	if author["name"] != "Ada" {
		t.Fatalf("author mapping = %#v", author)
	}
	backups, ok := data["video_backup"].([]any)
	if !ok {
		t.Fatalf("video_backup missing from %s", result.Body)
	}
	if len(backups) != 2 || backups[0].(map[string]any)["url"] != "a.mp4" || backups[1].(map[string]any)["url"] != "b.mp4" {
		t.Fatalf("backup mapping = %#v", backups)
	}
	if _, exists := body["payload"]; exists {
		t.Fatalf("source wrapper was not removed: %#v", body)
	}
}

func TestNormalizeMappingsValidatesPathWildcards(t *testing.T) {
	if _, err := NormalizeMappings([]FieldMapping{{
		Source: "items[].url",
		Target: "data.url",
	}}); err == nil {
		t.Fatal("wildcard mismatch was accepted")
	}
	if _, err := NormalizeMappings([]FieldMapping{{
		Source: "items[broken].url",
		Target: "data.items[].url",
	}}); err == nil {
		t.Fatal("invalid array selector was accepted")
	}
}
