// Package jsonappend provides helpers for appending fields to existing
// JSON objects without re-marshaling, preserving the original key order.
// Standard json.Marshal of map[string]any sorts keys alphabetically,
// which defeats field-ordering expectations set by the response contract.
package jsonappend

import (
	"bytes"
	"encoding/json"
)

// AppendObjects applies fn to a JSON object (first top-level '{'…'}' only),
// letting the caller append or replace fields while preserving the order
// of every key that fn does not touch. If the input is not a JSON object
// (an array, string, or scalar), it is returned unchanged.
func AppendObjects(src []byte, fn func(key []byte, rawValue json.RawMessage, buf *bytes.Buffer)) []byte {
	src = bytes.TrimSpace(src)
	if len(src) == 0 || src[0] != '{' {
		return src
	}
	dst := &bytes.Buffer{}
	// Use a streaming decoder to walk the object token-by-token, preserving
	// the raw bytes of each key-value pair we choose to retain.
	dec := json.NewDecoder(bytes.NewReader(src))
	// Read the opening {
	if _, err := dec.Token(); err != nil {
		return src
	}
	dst.WriteByte('{')
	first := true
	for dec.More() {
		// Read key token (returns string)
		keyToken, err := dec.Token()
		if err != nil {
			return src
		}
		key := []byte(keyToken.(string))
		// Read raw value bytes
		var rawValue json.RawMessage
		if err := dec.Decode(&rawValue); err != nil {
			return src
		}
		field := &bytes.Buffer{}
		fn(key, rawValue, field)
		if field.Len() == 0 {
			continue
		}
		if !first {
			dst.WriteByte(',')
		}
		dst.Write(field.Bytes())
		first = false
	}
	dst.WriteByte('}')
	return dst.Bytes()
}

// AppendFieldsAtEnd appends the given key-value pairs after all existing
// fields of a JSON object. Each pair is a (key, JSON-serializable value).
// Fields whose key already exists in the source are silently replaced.
func AppendFieldsAtEnd(src []byte, fields ...Field) []byte {
	skipKeys := make(map[string]bool, len(fields))
	for _, f := range fields {
		skipKeys[f.Key] = true
	}
	base := AppendObjects(src, func(key []byte, rawValue json.RawMessage, buf *bytes.Buffer) {
		if skipKeys[string(key)] {
			return // drop existing field with this key
		}
		keyJSON, _ := json.Marshal(string(key))
		buf.Write(keyJSON)
		buf.WriteByte(':')
		buf.Write(rawValue)
	})
	if len(base) < 2 || base[0] != '{' || base[len(base)-1] != '}' {
		return src
	}
	buf := bytes.NewBuffer(make([]byte, 0, len(base)+len(fields)*32))
	buf.Write(base[:len(base)-1])
	hasFields := len(base) > 2
	for _, field := range fields {
		if hasFields {
			buf.WriteByte(',')
		}
		field.writeWithoutComma(buf)
		hasFields = true
	}
	buf.WriteByte('}')
	return buf.Bytes()
}

// Field is a key-value pair to append or replace in a JSON object.
type Field struct {
	Key   string
	Value any
}

func (f Field) writeTo(buf *bytes.Buffer) {
	buf.WriteByte(',')
	f.writeWithoutComma(buf)
}

func (f Field) writeWithoutComma(buf *bytes.Buffer) {
	keyJSON, _ := json.Marshal(f.Key)
	buf.Write(keyJSON)
	buf.WriteByte(':')
	valueJSON, err := json.Marshal(f.Value)
	if err != nil {
		buf.WriteString("null")
	} else {
		buf.Write(valueJSON)
	}
}

// AppendTailFields appends key-value pairs at the very end of a JSON object
// that has already been built. Unlike AppendFieldsAtEnd, it does not filter
// existing keys — it purely appends. Use this for injecting platform fields
// (like _advertisement, _bugpkCache) that should always appear last.
func AppendTailFields(src []byte, fields ...Field) []byte {
	return AppendFieldsAtEnd(bytes.TrimSpace(src), fields...)
}
