// Package locale 负责语言标签归一化与 Accept-Language 解析。
// 它不决定站点支持哪些语言，由调用方用自己的启用语言列表做最终匹配。
package locale

import (
	"math"
	"sort"
	"strconv"
	"strings"
)

// NormalizeTag 归一化单个 BCP-47 风格标签；非法输入返回空串，
// 便于调用方继续沿回退链查找。
func NormalizeTag(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}
	if separator := strings.IndexByte(value, ';'); separator >= 0 {
		value = strings.TrimSpace(value[:separator])
	}
	value = strings.ReplaceAll(value, "_", "-")
	parts := strings.Split(value, "-")
	if len(parts) == 0 || len(parts) > 5 {
		return ""
	}

	language := parts[0]
	if len(language) < 2 || len(language) > 3 || !letters(language) {
		return ""
	}
	canonical := []string{strings.ToLower(language)}

	for _, part := range parts[1:] {
		switch {
		case len(part) == 4 && letters(part):
			canonical = append(canonical, strings.ToUpper(part[:1])+strings.ToLower(part[1:]))
		case len(part) == 2 && letters(part):
			canonical = append(canonical, strings.ToUpper(part))
		case len(part) == 3 && digits(part):
			canonical = append(canonical, part)
		case len(part) >= 4 && alphanumeric(part):
			canonical = append(canonical, strings.ToLower(part))
		default:
			return ""
		}
	}
	return strings.Join(canonical, "-")
}

// PrimaryLanguage 返回归一化标签的主语言子标签（小写）。
func PrimaryLanguage(tag string) string {
	if separator := strings.IndexByte(tag, '-'); separator >= 0 {
		return strings.ToLower(tag[:separator])
	}
	return strings.ToLower(tag)
}

// ParseAcceptLanguage 按 q 值返回归一化后的语言标签。
// q=0、非法项会被丢弃；重复项保留首次出现的位置。
func ParseAcceptLanguage(raw string) []string {
	type preference struct {
		order int
		q     float64
		tag   string
	}

	preferences := make([]preference, 0, 4)
	for order, chunk := range strings.Split(raw, ",") {
		part := strings.TrimSpace(chunk)
		if part == "" {
			continue
		}
		tag := part
		q := 1.0
		if separator := strings.IndexByte(part, ';'); separator >= 0 {
			tag = strings.TrimSpace(part[:separator])
			var ok bool
			q, ok = parseQuality(part[separator+1:])
			if !ok {
				continue
			}
		}
		if q <= 0 {
			continue
		}
		normalized := NormalizeTag(tag)
		if normalized == "" {
			continue
		}
		preferences = append(preferences, preference{tag: normalized, q: q, order: order})
	}

	sort.SliceStable(preferences, func(i, j int) bool {
		if preferences[i].q == preferences[j].q {
			return preferences[i].order < preferences[j].order
		}
		return preferences[i].q > preferences[j].q
	})

	result := make([]string, 0, len(preferences))
	seen := make(map[string]struct{}, len(preferences))
	for _, item := range preferences {
		if _, ok := seen[item.tag]; ok {
			continue
		}
		seen[item.tag] = struct{}{}
		result = append(result, item.tag)
	}
	return result
}

func parseQuality(raw string) (float64, bool) {
	quality := 1.0
	found := false
	for _, parameter := range strings.Split(raw, ";") {
		parameter = strings.TrimSpace(parameter)
		key, value, ok := strings.Cut(parameter, "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(key), "q") {
			continue
		}
		if found {
			return 0, false
		}
		found = true
		parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) || parsed < 0 || parsed > 1 {
			return 0, false
		}
		quality = parsed
	}
	return quality, true
}

func letters(value string) bool {
	for _, char := range value {
		if !((char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z')) {
			return false
		}
	}
	return value != ""
}

func digits(value string) bool {
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return value != ""
}

func alphanumeric(value string) bool {
	for _, char := range value {
		if !((char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9')) {
			return false
		}
	}
	return value != ""
}
