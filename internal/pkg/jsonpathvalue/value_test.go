package jsonpathvalue

import (
	"encoding/json"
	"testing"
)

func TestLookupSupportsArraysAndPreservesLargeNumbers(t *testing.T) {
	value, ok := Lookup(`{"data":{"items":[{"id":9007199254740993}]}}`, "data.items.0.id")
	if !ok {
		t.Fatal("array path lookup failed")
	}
	number, ok := value.(json.Number)
	if !ok || number.String() != "9007199254740993" {
		t.Fatalf("large number = %T %v", value, value)
	}
}

func TestLookupRejectsMissingNullAndAmbiguousJSON(t *testing.T) {
	for _, item := range []struct {
		body string
		path string
	}{
		{`{"data":{}}`, "data.code"},
		{`{"data":{"code":null}}`, "data.code"},
		{`{"data":{"code":1,"code":2}}`, "data.code"},
		{`{"data":[1]}`, "data.2"},
		{`{"data":{"code":1}} trailing`, "data.code"},
	} {
		if value, ok := Lookup(item.body, item.path); ok {
			t.Fatalf("Lookup(%q, %q) = %v, true", item.body, item.path, value)
		}
	}
}
