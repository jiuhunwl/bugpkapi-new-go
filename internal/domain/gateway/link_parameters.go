package gateway

import (
	"bugpk-api/internal/pkg/linkparam"
	"encoding/json"
	"net/url"
	"strings"
)

// Clean a private copy before validation/forwarding/logging. Never touch headers
// or key/signature fields. Invalid bodies are left to the existing strict parser.
func cleanOpenParameters(req OpenRequest) OpenRequest {
	cleanValues := func(values map[string][]string) (url.Values, bool) {
		out := make(url.Values, len(values))
		changed := false
		for name, items := range values {
			out[name] = append([]string(nil), items...)
			if linkparam.ProtectedName(name) {
				continue
			}
			for i, value := range items {
				next := linkparam.Clean(value)
				out[name][i] = next
				changed = changed || next != value
			}
		}
		return out, changed
	}
	if next, changed := cleanValues(req.Query); changed {
		req.Query = next
		req.RawQuery = next.Encode()
	}
	contentType := strings.ToLower(strings.TrimSpace(strings.SplitN(headerValue(req.Headers, "Content-Type"), ";", 2)[0]))
	if len(req.Body) > 0 && isJSONContentType(contentType) {
		if value, err := decodeUnambiguousJSON(req.Body); err == nil {
			changed := linkparam.CleanValue(value)
			if text, ok := value.(string); ok {
				next := linkparam.Clean(text)
				changed = next != text
				value = next
			}
			if changed {
				if body, err := json.Marshal(value); err == nil {
					req.Body = body
				}
			}
		}
	} else if contentType == "application/x-www-form-urlencoded" {
		if values, err := url.ParseQuery(string(req.Body)); err == nil {
			if next, changed := cleanValues(values); changed {
				req.Body = []byte(next.Encode())
			}
		}
	}
	return req
}
