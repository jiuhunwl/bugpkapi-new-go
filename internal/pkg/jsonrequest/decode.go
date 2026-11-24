package jsonrequest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const (
	DefaultMaxDepth       = 64
	DefaultMaxNumberBytes = 1024
	DefaultMaxValueCount  = 65_536
)

type decodeState struct {
	maxDepth int
	values   int
}

func DecodeUnambiguous(data []byte, maxDepth int) (any, error) {
	if maxDepth <= 0 {
		return nil, errors.New("JSON nesting limit must be positive")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	state := decodeState{maxDepth: maxDepth}
	value, err := state.decodeValue(decoder, 0)
	if err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("JSON包含多个顶层值")
		}
		return nil, fmt.Errorf("JSON尾随内容不合法: %w", err)
	}
	return value, nil
}

func UnmarshalUnambiguous(data []byte, target any, maxDepth int) error {
	if _, err := DecodeUnambiguous(data, maxDepth); err != nil {
		return err
	}
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("decode JSON target: %w", err)
	}
	return nil
}

func (s *decodeState) decodeValue(decoder *json.Decoder, depth int) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, fmt.Errorf("JSON语法错误: %w", err)
	}
	s.values++
	if s.values > DefaultMaxValueCount {
		return nil, fmt.Errorf("JSON值数量不能超过%d个", DefaultMaxValueCount)
	}
	delimiter, isDelimiter := token.(json.Delim)
	if !isDelimiter {
		if number, ok := token.(json.Number); ok && len(number.String()) > DefaultMaxNumberBytes {
			return nil, fmt.Errorf("JSON数字长度不能超过%d字节", DefaultMaxNumberBytes)
		}
		return token, nil
	}
	if depth >= s.maxDepth {
		return nil, fmt.Errorf("JSON嵌套层级不能超过%d层", s.maxDepth)
	}
	switch delimiter {
	case '{':
		result := map[string]any{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return nil, fmt.Errorf("JSON字段名不合法: %w", err)
			}
			key, ok := keyToken.(string)
			if !ok {
				return nil, errors.New("JSON对象字段名必须是字符串")
			}
			if _, exists := result[key]; exists {
				return nil, fmt.Errorf("JSON字段重复: %s", truncateRunes(key, 64))
			}
			value, err := s.decodeValue(decoder, depth+1)
			if err != nil {
				return nil, err
			}
			result[key] = value
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return nil, errors.New("JSON对象未正确结束")
		}
		return result, nil
	case '[':
		result := []any{}
		for decoder.More() {
			value, err := s.decodeValue(decoder, depth+1)
			if err != nil {
				return nil, err
			}
			result = append(result, value)
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return nil, errors.New("JSON数组未正确结束")
		}
		return result, nil
	default:
		return nil, errors.New("JSON结构不合法")
	}
}

func truncateRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}
