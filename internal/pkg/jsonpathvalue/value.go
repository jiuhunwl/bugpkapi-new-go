package jsonpathvalue

import (
	"strconv"
	"strings"

	"bugpk-api/internal/pkg/jsonrequest"
)

const maxJSONDepth = 64

func Lookup(body, path string) (any, bool) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, false
	}
	source, err := jsonrequest.DecodeUnambiguous([]byte(body), maxJSONDepth)
	if err != nil {
		return nil, false
	}
	current := source
	for _, segment := range strings.Split(path, ".") {
		if segment == "" {
			return nil, false
		}
		switch typed := current.(type) {
		case map[string]any:
			value, ok := typed[segment]
			if !ok {
				return nil, false
			}
			current = value
		case []any:
			index, err := strconv.Atoi(segment)
			if err != nil || index < 0 || index >= len(typed) {
				return nil, false
			}
			current = typed[index]
		default:
			return nil, false
		}
	}
	if current == nil {
		return nil, false
	}
	return current, true
}
