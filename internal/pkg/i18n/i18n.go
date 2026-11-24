// Package i18n 存放业务领域与翻译字典实现共享的窄接口和 DTO。
// 把这些类型放在独立包中，避免领域包与 translation 领域形成循环依赖。
package i18n

import "context"

// Localizer 负责后端业务数据的译文解析。实现必须并发安全；
// 当前语言没有译文时返回 nil。
type Localizer interface {
	Locale(ctx context.Context) string
	Localize(ctx context.Context, resourceType, key string) map[string]string
	LocalizeBatch(ctx context.Context, resourceType string, keys []string) map[string]map[string]string
}

// LocaleResolver 解析请求最终使用的语言。
// 优先级：显式查询参数 > X-App-Locale > Accept-Language。
type LocaleResolver interface {
	Resolve(explicit, appLocale, acceptLanguage string) string
}

type KeyKind string

const (
	KeyNumeric   KeyKind = "numeric"
	KeySingleton KeyKind = "singleton"
	KeyString    KeyKind = "string"
)

// Field 描述一个可翻译字段。
type Field struct {
	Name     string `json:"name"`
	LabelKey string `json:"labelKey"`
	MaxBytes int    `json:"maxBytes"`
	HTML     bool   `json:"html"`
}

// Resource 描述一种可翻译资源。
type Resource struct {
	Type       string  `json:"type"`
	LabelKey   string  `json:"labelKey"`
	KeyKind    KeyKind `json:"-"`
	Permission string  `json:"-"`
	Fields     []Field `json:"fields"`
}

// BaseResource 是单条记录的默认语言原文。
type BaseResource struct {
	Key         string
	DisplayName string
	Values      map[string]string
}

type ListQuery struct {
	Keyword  string
	Page     int
	PageSize int
	Locale   string
}

type PageResult[T any] struct {
	Items []T   `json:"items"`
	Total int64 `json:"total"`
}

// ResourceProvider 为后台字典页提供只读的默认语言原文。
type ResourceProvider interface {
	Type() string
	List(ctx context.Context, query ListQuery) (PageResult[BaseResource], error)
	Get(ctx context.Context, key string) (BaseResource, error)
}

type localeContextKey struct{}

// WithLocale 把解析后的语言写入请求上下文。
func WithLocale(ctx context.Context, locale string) context.Context {
	return context.WithValue(ctx, localeContextKey{}, locale)
}

// LocaleFromContext 返回上下文中的语言；不存在时返回空串。
func LocaleFromContext(ctx context.Context) string {
	value, _ := ctx.Value(localeContextKey{}).(string)
	return value
}
