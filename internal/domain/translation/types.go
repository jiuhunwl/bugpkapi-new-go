package translation

import "bugpk-api/internal/domain/rbac"

type Principal = rbac.Principal

type Locale struct {
	Code         string `json:"code"`
	Label        string `json:"label"`
	FallbackCode string `json:"fallbackCode,omitempty"`
	Enabled      bool   `json:"enabled"`
	IsDefault    bool   `json:"isDefault"`
	SortNo       int    `json:"sortNo"`
}

type SaveLocalesRequest struct {
	Locales []Locale `json:"locales"`
}

type ResourceTypeInfo struct {
	Type     string      `json:"type"`
	LabelKey string      `json:"labelKey"`
	Fields   []FieldInfo `json:"fields"`
	// Count 是该资源源表的条目总数，前端用它在下拉里展示规模，
	// 避免单例资源（如站点配置只有 1 行）被误解成「字典只有一条」。
	Count int64 `json:"count"`
}

type FieldInfo struct {
	Name     string `json:"name"`
	LabelKey string `json:"labelKey"`
	MaxBytes int    `json:"maxBytes"`
	HTML     bool   `json:"html"`
}

type Completeness struct {
	Translated int `json:"translated"`
	Total      int `json:"total"`
}

type ResourceItem struct {
	ResourceType string                       `json:"resourceType"`
	ResourceKey  string                       `json:"resourceKey"`
	DisplayName  string                       `json:"displayName"`
	BaseLocale   string                       `json:"baseLocale"`
	Fields       []string                     `json:"fields"`
	Values       map[string]string            `json:"values"`
	Translations map[string]map[string]string `json:"translations"`
	Completeness map[string]Completeness      `json:"completeness"`
	StaleFields  map[string][]string          `json:"staleFields,omitempty"`
}

type ResourceList struct {
	Items []ResourceItem `json:"items"`
	Total int64          `json:"total"`
}

type SaveTranslationsRequest struct {
	Translations map[string]map[string]string `json:"translations"`
}

type SaveTranslationsResult struct {
	Saved   int `json:"saved"`
	Deleted int `json:"deleted"`
}
