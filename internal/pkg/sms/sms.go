// Package sms 是短信验证码的通道层：统一 Provider 接口、配置解析与错误分类，
// 不做限额、熔断与验证码生成（那些属于 auth 域）。
package sms

import (
	"context"
	"strings"

	"bugpk-api/internal/pkg/phoneutil"
)

// Provider 标识，与管理端 bp_message_provider_config.provider_key 一一对应。
const (
	ProviderAliyun  = "aliyun"
	ProviderTencent = "tencent"
)

const (
	minCodeLen  = 4
	maxCodeLen  = 8
	maxSceneLen = 32
	// localeFallback 是模板语言缺省时的键，对应"未选择语言"的默认模板。
	localeFallback = ""
)

// Message 是一条待发验证码。To 必须是 phoneutil 归一化后的 E.164：
// 通道层不接受用户原始输入，避免把分隔符带进上游号码校验而白白消耗一次发送。
type Message struct {
	To     string
	Scene  string
	Locale string
	Code   string
}

// Receipt 是上游受理回执。受理不等于送达，送达要看后续回执或查询接口。
type Receipt struct {
	Provider  string
	RequestID string
	BizID     string
}

// Capabilities 描述通道能力，供管理端与路由决策使用。
type Capabilities struct {
	// International 表示该通道可发到 +86 之外的号码。
	International bool
	// DeliveryReport 表示可查询送达回执。
	DeliveryReport bool
	// NumberAuth 表示支持运营商一键登录。本期一律 false，
	// 字段先留在接口上，接入时不用改签名。
	NumberAuth bool
}

// Provider 发送一条验证码。实现必须自身保证不阻塞超过配置里的超时。
type Provider interface {
	Key() string
	Capabilities() Capabilities
	Send(ctx context.Context, msg Message) (Receipt, error)
}

// Class 是上游返回码的归类，决定上层是重试、切备用通道还是直接失败。
type Class int

const (
	// ClassOK 受理成功。
	ClassOK Class = iota
	// ClassPermanent 参数、签名、模板、账号或资质问题：重试无意义，切通道也无意义。
	ClassPermanent
	// ClassRetryable 上游过载、限流或网络问题：可重试，也应触发主备切换。
	ClassRetryable
	// ClassUnknown 未收录的返回码：按失败处理但不自动重试，等运营补表。
	ClassUnknown
)

func (c Class) String() string {
	switch c {
	case ClassOK:
		return "ok"
	case ClassPermanent:
		return "permanent"
	case ClassRetryable:
		return "retryable"
	default:
		return "unknown"
	}
}

// Error 是通道层的唯一错误形态。
//
// 错误串里既不出现完整号码，也不出现密钥：号码会被写进接口日志与告警，
// 密钥一旦进日志就等于永久泄露。detail 在进入 Error 前已由 newError 脱敏。
type Error struct {
	Provider string
	Code     string
	Detail   string
	Class    Class
	Err      error
}

func (e *Error) Error() string {
	if e == nil {
		return "sms: <nil>"
	}
	var builder strings.Builder
	builder.WriteString("sms: ")
	builder.WriteString(e.Provider)
	builder.WriteString(" send failed")
	if e.Code != "" {
		builder.WriteString(" (")
		builder.WriteString(e.Code)
		builder.WriteString(")")
	}
	if e.Detail != "" {
		builder.WriteString(": ")
		builder.WriteString(e.Detail)
	}
	return builder.String()
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// IsRetryable 报告上层是否可以按策略重试或切换通道。
func (e *Error) IsRetryable() bool {
	return e != nil && (e.Class == ClassRetryable || e.Class == ClassUnknown)
}

// classFromHTTPStatus 在拿不到业务返回码时的兜底归类：429/5xx 可能是上游抖动，
// 其它 4xx 基本是凭证或参数问题，重试只会继续烧额度。
func classFromHTTPStatus(status int) Class {
	switch {
	case status == 429, status >= 500:
		return ClassRetryable
	case status >= 400:
		return ClassPermanent
	default:
		return ClassUnknown
	}
}

// newError 构造已脱敏的通道错误。
func newError(provider, code, detail string, class Class, err error, sanitize ...func(string) string) *Error {
	for _, fn := range sanitize {
		if fn != nil {
			detail = fn(detail)
		}
	}
	return &Error{Provider: provider, Code: code, Detail: redactSecret(detail), Class: class, Err: err}
}

// maskNumber 把 detail 里出现的号码换成脱敏形式。上游偶尔会把请求参数回显在
// 错误信息里，接口日志随即就会带着明文号码落库。
func maskNumber(detail, to string) string {
	if detail == "" || to == "" {
		return detail
	}
	digits := phoneutil.Digits(to)
	if digits == "" {
		return detail
	}
	trimmed := strings.TrimPrefix(to, "+")
	for _, candidate := range []string{to, trimmed, digits} {
		if len(candidate) >= 6 {
			detail = strings.ReplaceAll(detail, candidate, phoneutil.Mask(to))
		}
	}
	return detail
}

// validateMessage 在出网之前做输入检查。
func validateMessage(msg Message) error {
	if !phoneutil.IsValid(msg.To) {
		return &Error{Provider: "sms", Code: "invalid_recipient", Class: ClassPermanent,
			Detail: "recipient must be a normalized E.164 number"}
	}
	if msg.Scene == "" || len(msg.Scene) > maxSceneLen {
		return &Error{Provider: "sms", Code: "invalid_scene", Class: ClassPermanent,
			Detail: "scene is required and must be at most 32 characters"}
	}
	if len(msg.Code) < minCodeLen || len(msg.Code) > maxCodeLen {
		return &Error{Provider: "sms", Code: "invalid_code", Class: ClassPermanent,
			Detail: "code length must be between 4 and 8 digits"}
	}
	for i := 0; i < len(msg.Code); i++ {
		if msg.Code[i] < '0' || msg.Code[i] > '9' {
			return &Error{Provider: "sms", Code: "invalid_code", Class: ClassPermanent,
				Detail: "code must contain digits only"}
		}
	}
	return nil
}

// selectTemplate 按语言选模板，缺失时回退默认模板。
// 与邮箱模板同规则：未配置英文不代表英文用户不该收到码。
func selectTemplate(defaultTemplate string, byLocale map[string]string, locale string) (string, bool) {
	locale = normalizeLocale(locale)
	if byLocale != nil {
		if template := strings.TrimSpace(byLocale[locale]); template != "" {
			return template, true
		}
	}
	if strings.TrimSpace(defaultTemplate) != "" {
		return strings.TrimSpace(defaultTemplate), true
	}
	return "", false
}

func normalizeLocale(locale string) string {
	locale = strings.ToLower(strings.TrimSpace(locale))
	if locale == "" {
		return localeFallback
	}
	if index := strings.IndexAny(locale, "-_"); index > 0 {
		locale = locale[:index]
	}
	return locale
}
