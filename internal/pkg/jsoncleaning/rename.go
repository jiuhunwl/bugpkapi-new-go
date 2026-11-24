// Package jsoncleaning applies administrator-configured field aliases and
// JSON paths to upstream responses before public response normalization.
package jsoncleaning

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"bugpk-api/internal/pkg/jsonrequest"
)

const (
	MaxFieldNameRunes = 128
	MaxMappings       = 50
)

// FieldMapping renames an upstream field/path to a canonical response path.
// Plain names retain the legacy recursive-key behavior. A path containing a
// dot, array selector, or $ prefix is resolved from the JSON root.
type FieldMapping struct {
	Source string `json:"source"`
	Target string `json:"target"`
}

// Result describes a field-renaming attempt.
type Result struct {
	Body    []byte
	Changed bool
	IsJSON  bool
}

type pathSegmentKind uint8

const (
	pathKey pathSegmentKind = iota
	pathIndex
	pathWildcard
)

type pathSegment struct {
	index int
	key   string
	kind  pathSegmentKind
}

type pathMatch struct {
	concrete        []pathSegment
	value           any
	wildcardIndexes []int
}

// NormalizeMappings trims and validates a mapping list. Completely empty rows
// are ignored so an unfinished blank editor row does not make a save fail.
func NormalizeMappings(mappings []FieldMapping) ([]FieldMapping, error) {
	if len(mappings) > MaxMappings {
		return nil, fmt.Errorf("field mappings cannot exceed %d entries", MaxMappings)
	}
	normalized := make([]FieldMapping, 0, len(mappings))
	sources := make(map[string]struct{}, len(mappings))
	targets := make(map[string]struct{}, len(mappings))
	for _, mapping := range mappings {
		source := strings.TrimSpace(mapping.Source)
		target := strings.TrimSpace(mapping.Target)
		if source == "" && target == "" {
			continue
		}
		if source == "" || target == "" {
			return nil, errors.New("both source and target fields are required")
		}
		if utf8.RuneCountInString(source) > MaxFieldNameRunes || utf8.RuneCountInString(target) > MaxFieldNameRunes {
			return nil, fmt.Errorf("field mapping names cannot exceed %d characters", MaxFieldNameRunes)
		}

		var sourcePath, targetPath []pathSegment
		if isPathMapping(FieldMapping{Source: source, Target: target}) {
			var err error
			sourcePath, err = parseJSONPath(source)
			if err != nil {
				return nil, fmt.Errorf("invalid source JSON path %q: %w", source, err)
			}
			targetPath, err = parseJSONPath(target)
			if err != nil {
				return nil, fmt.Errorf("invalid target JSON path %q: %w", target, err)
			}
			if wildcardCount(sourcePath) != wildcardCount(targetPath) {
				return nil, errors.New("source and target JSON paths must use the same number of array wildcards")
			}
		}
		if source == target || (len(sourcePath) > 0 && pathsEqual(sourcePath, targetPath)) {
			return nil, fmt.Errorf("source and target cannot be the same: %q", source)
		}
		if _, exists := sources[source]; exists {
			return nil, fmt.Errorf("duplicate source field: %q", source)
		}
		if _, exists := targets[target]; exists {
			return nil, fmt.Errorf("duplicate target field: %q", target)
		}
		sources[source] = struct{}{}
		targets[target] = struct{}{}
		normalized = append(normalized, FieldMapping{Source: source, Target: target})
	}
	for _, mapping := range normalized {
		if _, chained := sources[mapping.Target]; chained {
			return nil, fmt.Errorf("field mappings cannot chain or cycle through %q", mapping.Target)
		}
	}
	return normalized, nil
}

// RenameFields supports both legacy recursive key aliases (for example
// media_link -> url) and explicit JSON paths (for example
// payload.author.nickname -> data.author.name). Array paths accept numeric
// indexes as well as [] / [*] wildcards. Existing canonical target values win.
func RenameFields(data []byte, mappings []FieldMapping) (Result, error) {
	original := append([]byte(nil), data...)
	if len(mappings) == 0 {
		return Result{Body: original}, nil
	}
	normalized, err := NormalizeMappings(mappings)
	if err != nil {
		return Result{}, err
	}
	if len(normalized) == 0 {
		return Result{Body: original}, nil
	}
	trimmed := bytes.TrimSpace(data)
	trimmed = bytes.TrimPrefix(trimmed, []byte{0xef, 0xbb, 0xbf})
	if len(trimmed) == 0 || (trimmed[0] != '{' && trimmed[0] != '[') {
		return Result{Body: original}, nil
	}
	value, err := jsonrequest.DecodeUnambiguous(trimmed, jsonrequest.DefaultMaxDepth)
	if err != nil {
		return Result{}, fmt.Errorf("decode upstream JSON for field mapping: %w", err)
	}

	legacyMappings := make([]FieldMapping, 0, len(normalized))
	pathMappings := make([]FieldMapping, 0, len(normalized))
	for _, mapping := range normalized {
		if isPathMapping(mapping) {
			pathMappings = append(pathMappings, mapping)
		} else {
			legacyMappings = append(legacyMappings, mapping)
		}
	}

	// Resolve explicit paths against an untouched copy. This keeps a wrapper
	// alias such as payload -> data from invalidating another path rule.
	sourceValue := value
	renamed, changed := renameValue(cloneJSONValue(value), legacyMappings)
	for _, mapping := range pathMappings {
		sourcePath, _ := parseJSONPath(mapping.Source)
		targetPath, _ := parseJSONPath(mapping.Target)
		for _, match := range collectPathMatches(sourceValue, sourcePath) {
			updated, targetChanged, accepted := setPathValue(
				renamed, targetPath, match.wildcardIndexes, match.value,
			)
			if !accepted {
				continue
			}
			renamed = updated
			sourceRemoved := deleteConcretePath(renamed, match.concrete)
			changed = changed || targetChanged || sourceRemoved
		}
	}
	if !changed {
		return Result{Body: original, IsJSON: true}, nil
	}
	encoded, err := marshalWithSourceOrder(renamed, trimmed, legacyMappings)
	if err != nil {
		return Result{}, fmt.Errorf("encode field-mapped upstream JSON: %w", err)
	}
	return Result{Body: encoded, Changed: true, IsJSON: true}, nil
}

type orderedSourceField struct {
	key   string
	value any
}

type orderedSourceObject []orderedSourceField

func marshalWithSourceOrder(value any, source []byte, mappings []FieldMapping) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(source))
	decoder.UseNumber()
	shape, err := decodeOrderedSource(decoder)
	if err != nil {
		return nil, err
	}
	aliases := make(map[string]string, len(mappings))
	for _, mapping := range mappings {
		aliases[mapping.Source] = mapping.Target
	}
	var output bytes.Buffer
	if err := writeValueInSourceOrder(&output, value, shape, aliases); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func decodeOrderedSource(decoder *json.Decoder) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delimiter, structured := token.(json.Delim)
	if !structured {
		return token, nil
	}
	switch delimiter {
	case '{':
		object := make(orderedSourceObject, 0)
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return nil, err
			}
			value, err := decodeOrderedSource(decoder)
			if err != nil {
				return nil, err
			}
			object = append(object, orderedSourceField{key: keyToken.(string), value: value})
		}
		_, err = decoder.Token()
		return object, err
	case '[':
		array := make([]any, 0)
		for decoder.More() {
			value, err := decodeOrderedSource(decoder)
			if err != nil {
				return nil, err
			}
			array = append(array, value)
		}
		_, err = decoder.Token()
		return array, err
	default:
		return nil, fmt.Errorf("unsupported JSON delimiter %q", delimiter)
	}
}

func writeValueInSourceOrder(output *bytes.Buffer, value, shape any, aliases map[string]string) error {
	switch current := value.(type) {
	case map[string]any:
		output.WriteByte('{')
		emitted := make(map[string]struct{}, len(current))
		first := true
		if objectShape, ok := shape.(orderedSourceObject); ok {
			for _, sourceField := range objectShape {
				key := sourceField.key
				if alias, exists := aliases[key]; exists {
					key = alias
				}
				child, exists := current[key]
				if !exists {
					child, exists = current[sourceField.key]
					key = sourceField.key
				}
				if !exists {
					continue
				}
				if _, exists := emitted[key]; exists {
					continue
				}
				if !first {
					output.WriteByte(',')
				}
				if err := writeOrderedField(output, key, child, sourceField.value, aliases); err != nil {
					return err
				}
				emitted[key] = struct{}{}
				first = false
			}
		}
		remaining := make([]string, 0, len(current)-len(emitted))
		for key := range current {
			if _, exists := emitted[key]; !exists {
				remaining = append(remaining, key)
			}
		}
		sort.Strings(remaining)
		for _, key := range remaining {
			if !first {
				output.WriteByte(',')
			}
			if err := writeOrderedField(output, key, current[key], nil, aliases); err != nil {
				return err
			}
			first = false
		}
		output.WriteByte('}')
		return nil
	case []any:
		output.WriteByte('[')
		shapeArray, _ := shape.([]any)
		for index, child := range current {
			if index > 0 {
				output.WriteByte(',')
			}
			var childShape any
			if index < len(shapeArray) {
				childShape = shapeArray[index]
			}
			if err := writeValueInSourceOrder(output, child, childShape, aliases); err != nil {
				return err
			}
		}
		output.WriteByte(']')
		return nil
	default:
		encoded, err := json.Marshal(value)
		if err != nil {
			return err
		}
		output.Write(encoded)
		return nil
	}
}

func writeOrderedField(output *bytes.Buffer, key string, value, shape any, aliases map[string]string) error {
	encodedKey, _ := json.Marshal(key)
	output.Write(encodedKey)
	output.WriteByte(':')
	return writeValueInSourceOrder(output, value, shape, aliases)
}

func isPathMapping(mapping FieldMapping) bool {
	return hasPathSyntax(mapping.Source) || hasPathSyntax(mapping.Target)
}

func hasPathSyntax(path string) bool {
	path = strings.TrimSpace(path)
	return strings.HasPrefix(path, "$") || strings.ContainsAny(path, ".[")
}

// parseJSONPath accepts dot paths (`data.author.name`), an optional `$` root,
// numeric indexes (`data.images.0`) and wildcard selectors (`items[].url`).
func parseJSONPath(path string) ([]pathSegment, error) {
	path = strings.TrimSpace(path)
	if path == "" || path == "$" {
		return nil, errors.New("path is empty or root-only")
	}
	if strings.HasPrefix(path, "$") {
		path = strings.TrimPrefix(path, "$")
		path = strings.TrimPrefix(path, ".")
	}
	if path == "" {
		return nil, errors.New("path is empty")
	}

	segments := make([]pathSegment, 0, strings.Count(path, ".")+1)
	for position := 0; position < len(path); {
		start := position
		for position < len(path) && path[position] != '.' && path[position] != '[' {
			position++
		}
		key := strings.TrimSpace(path[start:position])
		if key == "" {
			return nil, errors.New("path contains an empty field")
		}
		if index, err := strconv.Atoi(key); err == nil && index >= 0 {
			segments = append(segments, pathSegment{kind: pathIndex, index: index})
		} else {
			segments = append(segments, pathSegment{kind: pathKey, key: key})
		}

		for position < len(path) && path[position] == '[' {
			closing := strings.IndexByte(path[position:], ']')
			if closing < 0 {
				return nil, errors.New("array selector is not closed")
			}
			closing += position
			selector := strings.TrimSpace(path[position+1 : closing])
			switch selector {
			case "", "*":
				segments = append(segments, pathSegment{kind: pathWildcard})
			default:
				index, err := strconv.Atoi(selector)
				if err != nil || index < 0 {
					return nil, fmt.Errorf("unsupported array selector [%s]", selector)
				}
				segments = append(segments, pathSegment{kind: pathIndex, index: index})
			}
			position = closing + 1
		}
		if position == len(path) {
			break
		}
		if path[position] != '.' {
			return nil, fmt.Errorf("unexpected character %q", path[position])
		}
		position++
		if position == len(path) {
			return nil, errors.New("path cannot end with a dot")
		}
	}
	return segments, nil
}

func wildcardCount(path []pathSegment) int {
	count := 0
	for _, segment := range path {
		if segment.kind == pathWildcard {
			count++
		}
	}
	return count
}

func pathsEqual(left, right []pathSegment) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func cloneJSONValue(value any) any {
	switch current := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(current))
		for key, child := range current {
			result[key] = cloneJSONValue(child)
		}
		return result
	case []any:
		result := make([]any, len(current))
		for index, child := range current {
			result[index] = cloneJSONValue(child)
		}
		return result
	default:
		return value
	}
}

func collectPathMatches(root any, path []pathSegment) []pathMatch {
	var matches []pathMatch
	var walk func(any, int, []pathSegment, []int)
	walk = func(current any, offset int, concrete []pathSegment, wildcardIndexes []int) {
		if offset == len(path) {
			matches = append(matches, pathMatch{
				concrete:        append([]pathSegment(nil), concrete...),
				value:           current,
				wildcardIndexes: append([]int(nil), wildcardIndexes...),
			})
			return
		}
		segment := path[offset]
		switch segment.kind {
		case pathKey:
			object, ok := current.(map[string]any)
			if !ok {
				return
			}
			child, exists := object[segment.key]
			if exists {
				walk(child, offset+1, append(concrete, segment), wildcardIndexes)
			}
		case pathIndex:
			array, ok := current.([]any)
			if ok && segment.index >= 0 && segment.index < len(array) {
				walk(array[segment.index], offset+1, append(concrete, segment), wildcardIndexes)
			}
		case pathWildcard:
			array, ok := current.([]any)
			if !ok {
				return
			}
			for index, child := range array {
				resolved := pathSegment{kind: pathIndex, index: index}
				walk(child, offset+1, append(concrete, resolved), append(wildcardIndexes, index))
			}
		}
	}
	walk(root, 0, nil, nil)
	return matches
}

func setPathValue(root any, path []pathSegment, wildcardIndexes []int, value any) (any, bool, bool) {
	wildcardOffset := 0
	var set func(any, bool, int) (any, bool, bool)
	set = func(current any, exists bool, offset int) (any, bool, bool) {
		if offset == len(path) {
			if exists {
				return current, false, true
			}
			return cloneJSONValue(value), true, true
		}

		segment := path[offset]
		wildcardResolved := false
		if segment.kind == pathWildcard {
			if wildcardOffset >= len(wildcardIndexes) {
				return current, false, false
			}
			segment = pathSegment{kind: pathIndex, index: wildcardIndexes[wildcardOffset]}
			wildcardOffset++
			wildcardResolved = true
		}

		var updated any
		var changed, accepted bool
		switch segment.kind {
		case pathKey:
			var object map[string]any
			if !exists {
				object = map[string]any{}
			} else {
				var ok bool
				object, ok = current.(map[string]any)
				if !ok {
					return current, false, false
				}
			}
			child, childExists := object[segment.key]
			updated, changed, accepted = set(child, childExists, offset+1)
			if changed {
				object[segment.key] = updated
			}
			if wildcardResolved {
				wildcardOffset--
			}
			return object, changed, accepted
		case pathIndex:
			var array []any
			originalLength := 0
			if !exists {
				array = make([]any, segment.index+1)
			} else {
				var ok bool
				array, ok = current.([]any)
				if !ok {
					return current, false, false
				}
				originalLength = len(array)
			}
			if segment.index >= len(array) {
				array = append(array, make([]any, segment.index-len(array)+1)...)
			}
			childExists := segment.index < originalLength
			updated, changed, accepted = set(array[segment.index], childExists, offset+1)
			if changed {
				array[segment.index] = updated
			}
			if wildcardResolved {
				wildcardOffset--
			}
			return array, changed, accepted
		default:
			return current, false, false
		}
	}
	return set(root, true, 0)
}

func deleteConcretePath(root any, path []pathSegment) bool {
	if len(path) == 0 {
		return false
	}
	var remove func(any, int) (bool, bool)
	remove = func(current any, offset int) (bool, bool) {
		segment := path[offset]
		last := offset == len(path)-1
		switch segment.kind {
		case pathKey:
			object, ok := current.(map[string]any)
			if !ok {
				return false, false
			}
			if last {
				if _, exists := object[segment.key]; exists {
					delete(object, segment.key)
					return true, len(object) == 0
				}
				return false, len(object) == 0
			}
			child, exists := object[segment.key]
			if !exists {
				return false, len(object) == 0
			}
			removed, childEmpty := remove(child, offset+1)
			if childEmpty {
				delete(object, segment.key)
			}
			return removed, len(object) == 0
		case pathIndex:
			array, ok := current.([]any)
			if !ok || segment.index < 0 || segment.index >= len(array) {
				return false, false
			}
			// Removing an array element would shift every later wildcard match.
			if last {
				return false, emptyJSONArray(array)
			}
			removed, childEmpty := remove(array[segment.index], offset+1)
			if childEmpty {
				array[segment.index] = nil
			}
			return removed, emptyJSONArray(array)
		default:
			return false, false
		}
	}
	removed, _ := remove(root, 0)
	return removed
}

func emptyJSONArray(array []any) bool {
	for _, item := range array {
		switch value := item.(type) {
		case nil:
			continue
		case map[string]any:
			if len(value) == 0 {
				continue
			}
		case []any:
			if emptyJSONArray(value) {
				continue
			}
		}
		return false
	}
	return true
}

func renameValue(value any, mappings []FieldMapping) (any, bool) {
	switch current := value.(type) {
	case map[string]any:
		mappedSources := make(map[string]struct{}, len(mappings))
		for _, mapping := range mappings {
			mappedSources[mapping.Source] = struct{}{}
		}
		result := make(map[string]any, len(current))
		changed := false
		for key, child := range current {
			if _, mapped := mappedSources[key]; mapped {
				continue
			}
			renamedChild, childChanged := renameValue(child, mappings)
			result[key] = renamedChild
			changed = changed || childChanged
		}
		for _, mapping := range mappings {
			child, exists := current[mapping.Source]
			if !exists {
				continue
			}
			renamedChild, _ := renameValue(child, mappings)
			if _, targetExists := result[mapping.Target]; !targetExists {
				result[mapping.Target] = renamedChild
			}
			changed = true
		}
		return result, changed
	case []any:
		result := make([]any, len(current))
		changed := false
		for index, child := range current {
			renamedChild, childChanged := renameValue(child, mappings)
			result[index] = renamedChild
			changed = changed || childChanged
		}
		return result, changed
	default:
		return value, false
	}
}
