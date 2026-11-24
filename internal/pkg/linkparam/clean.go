// Package linkparam extracts an HTTP(S) URL from a pasted sharing message.
package linkparam

import (
	"encoding/json"
	"net/url"
	"regexp"
	"strings"
)

var links = regexp.MustCompile("(?i)https?://[^\\s<>\"'`，。；！？、…“”【】（）]+")

// ProtectedName prevents business-parameter cleanup from changing credentials.
func ProtectedName(name string) bool {
	name = strings.ToLower(strings.NewReplacer("_", "", "-", "", " ", "").Replace(name))
	switch name {
	case "key", "apikey", "xapikey", "authorization", "token", "accesstoken", "refreshtoken", "password", "secret", "secretkey", "sign", "signature", "nonce":
		return true
	}
	return false
}

// Clean returns the first valid URL without re-encoding it. Plain text and
// serialized objects/arrays remain byte-for-byte intact. This never fetches URLs.
func Clean(value string) string {
	text := strings.TrimSpace(value)
	if (strings.HasPrefix(text, "{") || strings.HasPrefix(text, "[")) && json.Valid([]byte(text)) {
		return value
	}
	for _, candidate := range links.FindAllString(value, -1) {
		for _, pair := range [][2]string{{"(", ")"}, {"[", "]"}, {"{", "}"}} {
			for strings.HasSuffix(candidate, pair[1]) && strings.Count(candidate, pair[1]) > strings.Count(candidate, pair[0]) {
				candidate = strings.TrimSuffix(candidate, pair[1])
			}
		}
		if !strings.ContainsAny(candidate, "?#") {
			candidate = strings.TrimRight(candidate, ".,;!")
		}
		parsed, err := url.Parse(candidate)
		if err == nil && parsed.Hostname() != "" && parsed.User == nil && (strings.EqualFold(parsed.Scheme, "http") || strings.EqualFold(parsed.Scheme, "https")) && !strings.ContainsAny(parsed.Host, "\\ ") {
			return candidate
		}
	}
	return value
}

// CleanValue only changes string values; JSON keys, numbers and structure stay intact.
func CleanValue(value any) bool {
	changed := false
	switch data := value.(type) {
	case map[string]any:
		for key, item := range data {
			if ProtectedName(key) {
				continue
			}
			if text, ok := item.(string); ok {
				next := Clean(text)
				if next != text {
					data[key] = next
					changed = true
				}
			} else if CleanValue(item) {
				changed = true
			}
		}
	case []any:
		for i, item := range data {
			if text, ok := item.(string); ok {
				next := Clean(text)
				if next != text {
					data[i] = next
					changed = true
				}
			} else if CleanValue(item) {
				changed = true
			}
		}
	}
	return changed
}
