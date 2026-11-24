// Package orderedjson provides helpers for building JSON objects and arrays
// with deterministic key order. The standard library json.Marshal sorts map
// keys alphabetically, which is undesirable for API responses where field
// order should follow the upstream format or a documented contract.
package orderedjson

import (
	"bytes"
	"encoding/json"
	"strconv"
)

// Object builds a JSON object with keys in the order they are added.
// Each pair's value is marshaled independently; marshal errors are silently
// written as null. The caller is responsible for ensuring the final output
// is valid JSON.
type Object struct {
	buf  bytes.Buffer
	keys int
}

// NewObject creates a new ordered JSON object builder.
func NewObject() *Object {
	return &Object{}
}

// Set adds a key-value pair. The value is marshaled to JSON immediately.
// Returns the builder for chaining.
func (o *Object) Set(key string, value any) *Object {
	return o.SetRaw(key, marshalValue(value))
}

// SetRaw adds a key with pre-serialized JSON bytes. The caller guarantees
// rawValue is valid JSON.
func (o *Object) SetRaw(key string, rawValue []byte) *Object {
	if o.keys > 0 {
		o.buf.WriteByte(',')
	}
	keyBytes, _ := json.Marshal(key)
	o.buf.Write(keyBytes)
	o.buf.WriteByte(':')
	if len(rawValue) == 0 {
		o.buf.WriteString("null")
	} else {
		o.buf.Write(rawValue)
	}
	o.keys++
	return o
}

// Bytes returns the ordered JSON object as a byte slice. Returns "{}" if
// no fields were added; always valid JSON.
func (o *Object) Bytes() []byte {
	if o.keys == 0 {
		return []byte("{}")
	}
	buf := make([]byte, 1+o.buf.Len()+1)
	buf[0] = '{'
	copy(buf[1:], o.buf.Bytes())
	buf[len(buf)-1] = '}'
	return buf
}

// WriteTo writes the object to a buffer.
func (o *Object) WriteTo(dst *bytes.Buffer) {
	if o.keys == 0 {
		dst.WriteString("{}")
		return
	}
	dst.WriteByte('{')
	dst.Write(o.buf.Bytes())
	dst.WriteByte('}')
}

// Array builds a JSON array with elements in insertion order.
type Array struct {
	buf bytes.Buffer
}

// NewArray creates a new ordered JSON array builder.
func NewArray() *Array {
	return &Array{}
}

// Push adds a value to the end of the array.
func (a *Array) Push(value any) *Array {
	return a.PushRaw(marshalValue(value))
}

// PushRaw adds a pre-serialized JSON value.
func (a *Array) PushRaw(rawValue []byte) *Array {
	if a.buf.Len() > 0 {
		a.buf.WriteByte(',')
	}
	if len(rawValue) == 0 {
		a.buf.WriteString("null")
	} else {
		a.buf.Write(rawValue)
	}
	return a
}

// Bytes returns the ordered JSON array.
func (a *Array) Bytes() []byte {
	if a.buf.Len() == 0 {
		return []byte("[]")
	}
	buf := make([]byte, 1+a.buf.Len()+1)
	buf[0] = '['
	copy(buf[1:], a.buf.Bytes())
	buf[len(buf)-1] = ']'
	return buf
}

// Ints marshals a []int as a JSON array.
func Ints(values []int) []byte {
	a := NewArray()
	for _, v := range values {
		a.PushRaw([]byte(strconv.Itoa(v)))
	}
	return a.Bytes()
}

func marshalValue(value any) []byte {
	if value == nil {
		return []byte("null")
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return []byte("null")
	}
	return raw
}
