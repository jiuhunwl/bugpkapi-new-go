package auth

import "bugpk-api/internal/pkg/emaillang"

// ResolveEmailLang 从 Accept-Language 请求头解析邮件模板语言。
//
// 支持：
//   - 逗号分隔的多个语言与 q 权重（如 "zh-CN,zh;q=0.9,en;q=0.8"）
//   - 大小写不敏感（"ZH-CN" 等价于 "zh-cn"）
//   - 连字符/下划线均可（zh-CN、zh_CN 归一为 zh）
//
// 返回 "zh"、"en"，或空字符串（无法识别或未携带时返回空，调用方使用默认模板）。
func ResolveEmailLang(header string) string {
	return emaillang.Normalize(header)
}

// ResolveRequestEmailLang gives the language selected in the web UI priority
// over the browser's Accept-Language header. Unknown or missing preferences
// default to Chinese so a Chinese deployment never unexpectedly sends the
// English template merely because an intermediary removed the headers.
func ResolveRequestEmailLang(appLocale, acceptLanguage string) string {
	return emaillang.Resolve(appLocale, "", acceptLanguage, "")
}

// ResolveEmailLanguage is the shared decision boundary for email producers.
// explicitPageLanguage may be a request-body snapshot, while appLocale is the
// trusted current UI locale header supplied by the web client.
func ResolveEmailLanguage(
	explicitPageLanguage string,
	targetLanguage string,
	appLocale string,
	acceptLanguage string,
	siteLanguage string,
) string {
	pageLanguage := emaillang.FirstSupported(explicitPageLanguage, appLocale)
	return emaillang.Resolve(pageLanguage, targetLanguage, acceptLanguage, siteLanguage)
}
