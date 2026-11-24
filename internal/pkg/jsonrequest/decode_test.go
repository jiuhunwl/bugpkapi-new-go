package jsonrequest

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDecodeUnambiguousRejectsDuplicateFields(t *testing.T) {
	if _, err := DecodeUnambiguous([]byte(`{"username":"first","username":"second"}`), 16); err == nil || !strings.Contains(err.Error(), "JSON字段重复") {
		t.Fatalf("duplicate field error = %v", err)
	}
}

func TestDecodeUnambiguousRejectsExcessiveNestingAndTrailingValues(t *testing.T) {
	if _, err := DecodeUnambiguous([]byte(`[[[0]]]`), 2); err == nil || !strings.Contains(err.Error(), "JSON嵌套层级") {
		t.Fatalf("nesting error = %v", err)
	}
	if _, err := DecodeUnambiguous([]byte(`{} {}`), 16); err == nil {
		t.Fatal("multiple top-level values were accepted")
	}
}

func TestDecodeUnambiguousPreservesJSONNumbers(t *testing.T) {
	value, err := DecodeUnambiguous([]byte(`{"id":9007199254740993}`), 16)
	if err != nil {
		t.Fatal(err)
	}
	if number, ok := value.(map[string]any)["id"].(json.Number); !ok || number.String() != "9007199254740993" {
		t.Fatalf("large number was not preserved: %#v", value)
	}
}

func TestDecodeUnambiguousRejectsOversizedJSONNumber(t *testing.T) {
	body := []byte(`{"value":1e` + strings.Repeat("9", DefaultMaxNumberBytes) + `}`)
	_, err := DecodeUnambiguous(body, DefaultMaxDepth)
	if err == nil || !strings.Contains(err.Error(), "JSON数字长度") {
		t.Fatalf("oversized number error = %v", err)
	}
}

func TestDecodeUnambiguousRejectsExcessiveValueCount(t *testing.T) {
	body := []byte(`[` + strings.Repeat("0,", DefaultMaxValueCount) + `0]`)
	_, err := DecodeUnambiguous(body, DefaultMaxDepth)
	if err == nil || !strings.Contains(err.Error(), "JSON值数量") {
		t.Fatalf("excessive value count error = %v", err)
	}
}

func TestUnmarshalUnambiguousPopulatesTarget(t *testing.T) {
	var target struct {
		Name string `json:"name"`
	}
	if err := UnmarshalUnambiguous([]byte(`{"name":"BugPk-Api"}`), &target, DefaultMaxDepth); err != nil {
		t.Fatal(err)
	}
	if target.Name != "BugPk-Api" {
		t.Fatalf("target name = %q", target.Name)
	}
}

func FuzzDecodeUnambiguous(f *testing.F) {
	for _, seed := range [][]byte{
		[]byte(`null`),
		[]byte(`{"name":"BugPk-Api","enabled":true}`),
		[]byte(`[0,1.25,1e3,"text",false,null]`),
		[]byte(`{"duplicate":1,"duplicate":2}`),
		[]byte(`[[[[0]]]]`),
		{0xff, '{', '}', 0x00},
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 2*1024*1024 {
			t.Skip()
		}
		value, err := DecodeUnambiguous(data, DefaultMaxDepth)
		if err != nil {
			return
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("marshal accepted value: %v", err)
		}
		if _, err := DecodeUnambiguous(encoded, DefaultMaxDepth); err != nil {
			t.Fatalf("decode canonical value: %v; encoded=%q", err, encoded)
		}
	})
}
