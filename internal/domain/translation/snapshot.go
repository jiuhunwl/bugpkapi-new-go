package translation

import (
	"strings"

	"bugpk-api/internal/pkg/locale"
)

// snapshot 是全部启用译文的不可变预合并视图。
// 回退值在构建时解析，读路径只需一次 map 查找。
type snapshot struct {
	locales      []Locale
	defaultCode  string
	translations map[string]map[string]map[string]map[string]string
}

func newSnapshot(locales []Locale, entries []entry) *snapshot {
	enabled := make([]Locale, 0, len(locales))
	fallback := make(map[string]string, len(locales))
	defaultCode := ""
	for _, item := range locales {
		fallback[item.Code] = item.FallbackCode
		if item.IsDefault && defaultCode == "" {
			defaultCode = item.Code
		}
		if item.Enabled {
			enabled = append(enabled, item)
		}
	}
	if defaultCode == "" {
		defaultCode = "zh-CN"
	}

	raw := make(map[string]map[string]map[string]map[string]string)
	enabledCodes := make(map[string]struct{}, len(enabled))
	for _, item := range enabled {
		enabledCodes[item.Code] = struct{}{}
	}
	for _, item := range entries {
		if _, ok := enabledCodes[item.Locale]; !ok {
			continue
		}
		if raw[item.Locale] == nil {
			raw[item.Locale] = make(map[string]map[string]map[string]string)
		}
		if raw[item.Locale][item.ResourceType] == nil {
			raw[item.Locale][item.ResourceType] = make(map[string]map[string]string)
		}
		if raw[item.Locale][item.ResourceType][item.ResourceKey] == nil {
			raw[item.Locale][item.ResourceType][item.ResourceKey] = make(map[string]string)
		}
		raw[item.Locale][item.ResourceType][item.ResourceKey][item.Field] = item.Value
	}

	merged := make(map[string]map[string]map[string]map[string]string, len(enabled))
	for _, item := range enabled {
		merged[item.Code] = mergeFallback(item.Code, raw, fallback)
	}

	return &snapshot{locales: enabled, defaultCode: defaultCode, translations: merged}
}

func mergeFallback(start string, raw map[string]map[string]map[string]map[string]string, fallback map[string]string) map[string]map[string]map[string]string {
	result := make(map[string]map[string]map[string]string)
	visited := make(map[string]struct{}, 4)
	for code := start; code != ""; {
		if _, ok := visited[code]; ok {
			break
		}
		visited[code] = struct{}{}
		copyMissing(result, raw[code])
		code = fallback[code]
	}
	return result
}

func copyMissing(dst, src map[string]map[string]map[string]string) {
	for resourceType, keys := range src {
		if dst[resourceType] == nil {
			dst[resourceType] = make(map[string]map[string]string)
		}
		for key, fields := range keys {
			if dst[resourceType][key] == nil {
				dst[resourceType][key] = make(map[string]string, len(fields))
			}
			for field, value := range fields {
				if _, ok := dst[resourceType][key][field]; ok {
					continue
				}
				dst[resourceType][key][field] = value
			}
		}
	}
}

func (s *snapshot) Localize(localeCode, resourceType, key string) map[string]string {
	if s == nil {
		return nil
	}
	if localeCode == "" {
		localeCode = s.defaultCode
	}
	return s.translations[localeCode][resourceType][key]
}

func (s *snapshot) LocalizeBatch(localeCode, resourceType string, keys []string) map[string]map[string]string {
	if s == nil || len(keys) == 0 {
		return nil
	}
	if localeCode == "" {
		localeCode = s.defaultCode
	}
	byType := s.translations[localeCode][resourceType]
	if len(byType) == 0 {
		return nil
	}
	result := make(map[string]map[string]string, len(keys))
	for _, key := range keys {
		if fields, ok := byType[key]; ok && len(fields) > 0 {
			result[key] = fields
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func (s *snapshot) resolve(explicit, appLocale, acceptLanguage string) string {
	if s == nil {
		return "zh-CN"
	}
	candidates := make([]string, 0, 6)
	if explicit != "" {
		candidates = append(candidates, explicit)
	}
	if appLocale != "" {
		candidates = append(candidates, appLocale)
	}
	candidates = append(candidates, locale.ParseAcceptLanguage(acceptLanguage)...)
	for _, candidate := range candidates {
		if code := s.match(candidate); code != "" {
			return code
		}
	}
	return s.defaultCode
}

func (s *snapshot) match(raw string) string {
	tag := locale.NormalizeTag(raw)
	if tag == "" {
		return ""
	}
	for _, item := range s.locales {
		if strings.EqualFold(item.Code, tag) {
			return item.Code
		}
	}
	primary := locale.PrimaryLanguage(tag)
	for _, item := range s.locales {
		if item.IsDefault && locale.PrimaryLanguage(item.Code) == primary {
			return item.Code
		}
	}
	for _, item := range s.locales {
		if locale.PrimaryLanguage(item.Code) == primary {
			return item.Code
		}
	}
	return ""
}
