package masking

import (
	"encoding/json"
	"io"
	"net"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

const (
	redactedValue         = "[REDACTED]"
	redactedEndpointValue = "[UPSTREAM_REDACTED]"
)

var (
	errorBearerPattern = regexp.MustCompile(`(?i)(\bbearer\s+)[A-Za-z0-9._~+/=-]+`)
	errorCookiePattern = regexp.MustCompile(`(?i)(\b(?:cookie|set-cookie)\s*[:=]\s*)[^\r\n]+`)
	errorEmailPattern  = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)
	errorMobilePattern = regexp.MustCompile(`\b1[3-9][0-9]{9}\b`)
	// MySQL driver errors may echo the complete DSN, including account name,
	// password, host, port, database name, and query options. Treat the whole
	// tcp DSN as one endpoint so none of those fields survive in diagnostics.
	errorMySQLDSNPattern        = regexp.MustCompile(`(?i)\b[^\s"'<>:@]+(?::[^\s"'<>]*)?@tcp\([^\s"'<>)]*\)(?:/[^\s"'<>]*)?`)
	errorPEMPrivateKeyPattern   = regexp.MustCompile(`(?s)-----BEGIN [^-\r\n]*PRIVATE KEY-----.*?-----END [^-\r\n]*PRIVATE KEY-----`)
	errorSensitiveKeyPattern    = `\b(?:api[_-]?key|access[_-]?token|refresh[_-]?token|id[_-]?token|session[_-]?(?:id|key|token)|client[_-]?secret|webhook[_-]?secret|signing[_-]?key|authorization|auth[_-]?code|password|passwd|private[_-]?key|credential(?:s)?|pwd|secret(?:[_-]?key)?|token)\b`
	errorSensitiveDoublePattern = regexp.MustCompile(`(?is)(` + errorSensitiveKeyPattern + `\s*["']?\s*[:=]\s*)"(?:\\.|[^"\\])*"`)
	errorSensitiveSinglePattern = regexp.MustCompile(`(?is)(` + errorSensitiveKeyPattern + `\s*["']?\s*[:=]\s*)'(?:\\.|[^'\\])*'`)
	errorSensitivePairPattern   = regexp.MustCompile(`(?i)(` + errorSensitiveKeyPattern + `\s*["']?\s*[:=]\s*["']?)([^&,;\s"'}]+)`)
	// Error diagnostics can contain non-HTTP transports as well (for example
	// redis://, mysql://, grpc://, or tcp://).  Treat every URI scheme with an
	// authority/path delimiter as an endpoint here.  This broad error-only
	// matcher must not be reused by URLString, which intentionally preserves
	// configured/public URLs while masking only their sensitive query values.
	errorURLPattern = regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]{1,31}://[^\s"'<>]+`)
	// errorEncodedEndpointPattern intentionally accepts percent- or
	// JSON-unicode-encoded delimiters for common network schemes. Keep this
	// pass scoped to schemes that can occur in transport errors so source
	// frames such as "recovery_test.go:42" are not mistaken for a URI (the
	// normal URI matcher above still handles any scheme using ://).
	errorEncodedEndpointPattern = regexp.MustCompile(`(?i)\b(?:https?|wss?|ftp|ftps|redis|rediss|mysql|postgres(?:ql)?|amqps?|mongodb(?:\+srv)?|grpcs?|tcps?|udps?)(?:(?:%[0-9a-f]{2})|(?:\\u[0-9a-f]{4})|[:/])[^\s"'<>]+`)
	// Error strings may contain a URL that was escaped for JSON/log output or
	// percent-encoded as a query value. Keep these patterns separate from
	// URLString: error diagnostics must not retain the host, path, or query at
	// all, even when the URL itself contains no credentials.
	errorEscapedURLPattern   = regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]{1,31}:\\?/\\?/[^\s"'<>]+`)
	errorEncodedURLPattern   = regexp.MustCompile(`(?i)\b(?:https?|wss?|ftp|ftps|redis|rediss|mysql|postgres(?:ql)?|amqps?|mongodb(?:\+srv)?|grpcs?|tcps?|udps?)(?:%25)*%3a(?:%25)*%2f(?:%25)*%2f[^\s"'<>]+`)
	errorBareEndpointPattern = regexp.MustCompile(`(?i)(?:[a-z0-9](?:[a-z0-9-]{0,62}\.)+[a-z]{2,})(?::[0-9]{1,5})?(?:/[^\s"'<>]*)?`)
	// Schemeless single-label service names are common in container networks
	// (for example, "dial tcp api:8080/v1").  Require a port or path so normal
	// prose words are not treated as hosts; stack-specific matching below also
	// covers the no-path DNS form ("lookup localhost: no such host").
	errorSingleLabelEndpointPattern = regexp.MustCompile(`(?i)\b(?:localhost|[a-z](?:[a-z0-9-]{0,61}[a-z0-9])?):[0-9]{1,5}(?:/[^\s"'<>]*)?`)
	errorIPv4EndpointPattern        = regexp.MustCompile(`(?i)\b(?:[0-9]{1,3}\.){3}[0-9]{1,3}(?::[0-9]{1,5})?(?:/[^\s"'<>]*)?`)
	// Bracketed IPv6 is the unambiguous form emitted by net/http and net.Dial
	// (for example: dial tcp [2001:db8::1]:443: i/o timeout).
	errorIPv6EndpointPattern     = regexp.MustCompile(`(?i)\[[0-9a-f:]+\](?::[0-9]{1,5})?(?:/[^\s"'<>]*)?`)
	safeUpstreamReferencePattern = regexp.MustCompile(`^(?:node-[1-9][0-9]*)(?:,node-[1-9][0-9]*)*$`)
	// Stack traces commonly render DNS failures without a URL scheme, for
	// example "lookup api.example.com: no such host".  Match only host tokens
	// that follow an explicit network operation so source file names such as
	// "recovery_test.go:42" remain intact.
	stackNetworkHostPattern = regexp.MustCompile(`(?i)(\b(?:lookup|dial|connect(?:ing)?|resolve|host|server)\s+(?:(?:tcp|udp)\s+)?)(` +
		`(?:localhost|[a-z0-9](?:[a-z0-9-]{0,62}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,62}[a-z0-9])?){1,})(?::[0-9]{1,5})?(?:/[^\s"'<>]*)?` +
		`|[a-z](?:[a-z0-9-]{0,61}[a-z0-9]):[0-9]{1,5}(?:/[^\s"'<>]*)?` +
		`|[a-z](?:[a-z0-9-]{0,61}[a-z0-9]):` +
		`)`)
	// Resolver errors emitted by Go commonly put a single-label service name
	// before a qualifier instead of a colon, for example
	// "lookup api-backend on 127.0.0.11:53: no such host". Keep this pass
	// restricted to explicit DNS verbs/qualifiers so ordinary stack prose and
	// source-frame paths are not mistaken for endpoints.
	stackSingleLabelLookupPattern = regexp.MustCompile(`(?i)(\b(?:lookup|resolve|host)\s+)([a-z](?:[a-z0-9-]{0,61}[a-z0-9])?)(\s+(?:on|at|via|for|not)\b|:\s*|$)`)
	// A transport may print an unbracketed IPv6 address (especially when a
	// custom RoundTripper formats the error).  The token scanner below validates
	// candidates with net.ParseIP before replacing them, avoiding timestamps and
	// ordinary colon-separated diagnostics.
	looseIPv6TokenPattern   = regexp.MustCompile(`(?i)[0-9a-f:]{2,}(?:%[0-9a-z_.-]+)?(?:/[^\s"'<>]*)?`)
	internalIPv4Pattern     = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}(?::\d{1,5})?\b`)
	internalIPv6Pattern     = regexp.MustCompile(`(?i)(?:\[[0-9a-f:]+\](?::\d{1,5})?|(?:[0-9a-f]{0,4}:){2,}[0-9a-f]{0,4})`)
	internalHostnamePattern = regexp.MustCompile(`(?i)\b(?:localhost|[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.(?:cluster\.local|internal|lan|local|localhost|svc))(?::\d{1,5})?\b`)
)

var redactedKeyNames = map[string]bool{
	"accesstoken":   true,
	"apikey":        true,
	"authcode":      true,
	"authorization": true,
	"clientsecret":  true,
	"credential":    true,
	"credentials":   true,
	"cookie":        true,
	"idtoken":       true,
	"key":           true,
	"password":      true,
	"passwordhash":  true,
	"privatekey":    true,
	"pwd":           true,
	"refreshtoken":  true,
	"sessionid":     true,
	"sessionkey":    true,
	"sessiontoken":  true,
	"signingkey":    true,
	"secret":        true,
	"secretkey":     true,
	"token":         true,
	"webhooksecret": true,
	"xapikey":       true,
}

var emailKeyNames = map[string]bool{
	"email": true,
	"mail":  true,
}

var mobileKeyNames = map[string]bool{
	"mobile": true,
	"phone":  true,
	"tel":    true,
}

var internalRoutingKeyNames = map[string]bool{
	"backendaddress": true,
	"backendhost":    true,
	"backendurl":     true,
	"hostname":       true,
	"internalpath":   true,
	"internalroute":  true,
	"internalurl":    true,
	"originurl":      true,
	"proxyurl":       true,
	"requesturl":     true,
	"route":          true,
	"serveraddress":  true,
	"serverhost":     true,
	"serverpath":     true,
	"serviceurl":     true,
	"targeturl":      true,
	"upstream":       true,
	"upstreamhost":   true,
	"upstreampath":   true,
	"upstreamurl":    true,
}

func Email(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	parts := strings.Split(value, "@")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return middle(value, 2, 2)
	}
	local := parts[0]
	domain := parts[1]
	if len([]rune(local)) <= 2 {
		return firstRunes(local, 1) + "***@" + domain
	}
	return firstRunes(local, 2) + "***" + lastRunes(local, 1) + "@" + domain
}

// PasswordResetEmail reveals enough of a valid address for the user to
// recognize the delivery target while keeping the local part partially hidden.
// Invalid historical values always use the neutral placeholder.
func PasswordResetEmail(value string) string {
	if value == "" || strings.ContainsFunc(value, unicode.IsSpace) {
		return "***@***"
	}
	firstAt := strings.IndexByte(value, '@')
	if firstAt <= 0 || firstAt != strings.LastIndexByte(value, '@') || firstAt == len(value)-1 {
		return "***@***"
	}

	local := value[:firstAt]
	domain := value[firstAt+1:]
	localRunes := []rune(local)
	switch len(localRunes) {
	case 0:
		return "***@***"
	case 1, 2:
		return string(localRunes[:1]) + "***@" + domain
	case 3, 4:
		return string(localRunes[:1]) + "***" + string(localRunes[len(localRunes)-1:]) + "@" + domain
	default:
		return string(localRunes[:2]) + "***" + string(localRunes[len(localRunes)-1:]) + "@" + domain
	}
}

func Mobile(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	return middle(value, 3, 4)
}

func IP(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	ip := net.ParseIP(value)
	if ip == nil {
		return middle(value, 2, 2)
	}
	if v4 := ip.To4(); v4 != nil {
		parts := strings.Split(v4.String(), ".")
		if len(parts) == 4 {
			return parts[0] + "." + parts[1] + ".*.*"
		}
	}
	parts := strings.Split(ip.String(), ":")
	if len(parts) <= 2 {
		return ip.String() + ":***"
	}
	return parts[0] + ":" + parts[1] + ":***"
}

func SensitiveJSONText(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	var parsed any
	decoder := json.NewDecoder(strings.NewReader(value))
	decoder.UseNumber()
	if err := decoder.Decode(&parsed); err != nil {
		return value
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return value
	}
	masked := sensitiveJSONValue("", parsed)
	data, err := json.Marshal(masked)
	if err != nil {
		return value
	}
	return string(data)
}

func SensitiveValue(value any) any {
	return sensitiveJSONValue("", value)
}

// SensitiveJSONTextWithURLs 在 SensitiveJSONText 的基础上，对 JSON 内的字符串值
// 再做一次 URL 查询值脱敏（仅当字符串本身是 URL 时脱敏其查询中的敏感 key，
// 保留 scheme/host/path 与普通业务参数）。用于调用日志的 request_params /
// request_value 列：业务 URL（例如用户传入的商品链接）必须保留可读，
// 同时 URL 查询串里携带的凭证（token/apiKey/password 等）要被脱敏。
// 非 JSON 输入回退为普通 Text 处理（含 URL 查询敏感 key 脱敏）。
func SensitiveJSONTextWithURLs(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	var parsed any
	decoder := json.NewDecoder(strings.NewReader(value))
	decoder.UseNumber()
	if err := decoder.Decode(&parsed); err != nil {
		return sensitiveRequestURL(Text(value), 0)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return sensitiveRequestURL(Text(value), 0)
	}
	masked := sensitiveJSONWithURLValue("", parsed)
	data, err := json.Marshal(masked)
	if err != nil {
		return sensitiveRequestURL(Text(value), 0)
	}
	return string(data)
}

// sensitiveJSONWithURLValue 与 sensitiveJSONValue 行为一致，额外对字符串值
// 调用 URLString 以脱敏 URL 查询值中的敏感字段。
func sensitiveJSONWithURLValue(key string, value any) any {
	normalized := normalizeKey(key)
	switch typed := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for childKey := range typed {
			keys = append(keys, childKey)
		}
		sort.Strings(keys)
		result := make(map[string]any, len(typed))
		for _, childKey := range keys {
			result[childKey] = sensitiveJSONWithURLValue(childKey, typed[childKey])
		}
		return result
	case map[string][]string:
		keys := make([]string, 0, len(typed))
		for childKey := range typed {
			keys = append(keys, childKey)
		}
		sort.Strings(keys)
		result := make(map[string]any, len(typed))
		for _, childKey := range keys {
			result[childKey] = sensitiveJSONWithURLValue(childKey, typed[childKey])
		}
		return result
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			result[index] = sensitiveJSONWithURLValue(key, item)
		}
		return result
	case []string:
		result := make([]any, len(typed))
		for index, item := range typed {
			result[index] = sensitiveJSONWithURLValue(key, item)
		}
		return result
	case string:
		if isRedactedKey(normalized) {
			return redactedValue
		}
		if emailKeyNames[normalized] {
			return Email(typed)
		}
		if mobileKeyNames[normalized] {
			return Mobile(typed)
		}
		// 非敏感 key 的字符串值：若它本身是 URL，脱敏其查询值中的敏感字段，
		// 保留 scheme/host/path 与普通业务参数。
		return sensitiveRequestURL(typed, 0)
	default:
		if isRedactedKey(normalized) {
			return redactedValue
		}
		return typed
	}
}

// sensitiveRequestURL preserves caller-supplied HTTP URLs, including nested
// query URLs, without exposing credentials inside those nested URLs. The depth
// cap bounds work on adversarially nested input; diagnostics use ErrorText.
func sensitiveRequestURL(value string, depth int) string {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || (!strings.EqualFold(parsed.Scheme, "http") && !strings.EqualFold(parsed.Scheme, "https")) {
		return URLString(value)
	}
	if depth >= 8 {
		return redactedValue
	}
	if _, err := url.ParseQuery(parsed.RawQuery); err != nil {
		return redactedValue
	}
	safe := URLString(value)
	parsed, err = url.Parse(safe)
	if err != nil {
		return redactedValue
	}
	query := parsed.Query()
	changed := false
	for key, values := range query {
		for i, item := range values {
			cleaned := sensitiveRequestURL(item, depth+1)
			if cleaned != item {
				values[i] = cleaned
				changed = true
			}
		}
		query[key] = values
	}
	if !changed {
		return safe
	}
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

// UserVisibleJSONText prepares request/response payloads for ordinary-user
// call-log views. In addition to credential masking, it removes internal
// routing fields and private network locations so backend topology cannot be
// inferred from an otherwise valid business response.
func UserVisibleJSONText(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	var parsed any
	decoder := json.NewDecoder(strings.NewReader(value))
	decoder.UseNumber()
	if err := decoder.Decode(&parsed); err != nil {
		return ResponseText(value)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return ResponseText(value)
	}
	masked := userVisibleJSONValue("", sensitiveJSONValue("", parsed))
	data, err := json.Marshal(masked)
	if err != nil {
		return ResponseText(value)
	}
	return string(data)
}

// ResponseText 是调用日志响应体（response_body）的文本脱敏路径。
//
// 响应体是调用方本来就收到的业务数据（例如解析结果里的视频直链），不是
// 服务器内部的路由配置。把它当作端点诊断一律抹成 [UPSTREAM_REDACTED] 会让
// 调用日志彻底失去排查价值：生产实测 24 小时内 86.6% 的响应体被抹空，
// 运维无法判断用户拿到的是哪个 CDN 地址、为什么播放失败。
//
// 边界与调用方输入（requestParams/requestValue）保持一致：
//   - 保留公开 URL 的 scheme/host/path 与普通查询参数；
//   - 脱敏凭据、邮箱、手机号，以及 URL 查询值中的敏感 key；
//   - 内网/环回/私有地址替换为 [INTERNAL_REDACTED]，不泄露内部拓扑。
//
// 上游路由地址仍由独立的 upstream_url 列 + UpstreamReference 保护，诊断
// 字段 error_message 仍走 ErrorText 全量端点脱敏，三者职责分离。
func ResponseText(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	value = errorPEMPrivateKeyPattern.ReplaceAllString(value, redactedValue)
	value = errorMySQLDSNPattern.ReplaceAllStringFunc(value, redactEndpointCandidate)
	value = errorBearerPattern.ReplaceAllString(value, `${1}`+redactedValue)
	value = errorCookiePattern.ReplaceAllString(value, `${1}`+redactedValue)
	value = errorSensitiveDoublePattern.ReplaceAllString(value, `${1}`+redactedValue)
	value = errorSensitiveSinglePattern.ReplaceAllString(value, `${1}`+redactedValue)
	value = errorSensitivePairPattern.ReplaceAllString(value, `${1}`+redactedValue)
	value = errorEmailPattern.ReplaceAllStringFunc(value, Email)
	value = errorMobilePattern.ReplaceAllStringFunc(value, Mobile)
	return InternalNetworkText(value)
}

// ResponseJSONText 是响应体的 JSON 路径。与 ResponseText 的边界完全相同，
// 只是按结构逐个字段处理：敏感 key 的整个值替换为 [REDACTED]，其余字符串
// 值走 InternalNetworkText（保留公开 URL、抹掉内网地址、脱敏查询值中的
// 敏感 key）。非 JSON 输入回退到 ResponseText。
func ResponseJSONText(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	var parsed any
	decoder := json.NewDecoder(strings.NewReader(value))
	decoder.UseNumber()
	if err := decoder.Decode(&parsed); err != nil {
		return ResponseText(value)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return ResponseText(value)
	}
	data, err := json.Marshal(responseJSONValue("", parsed))
	if err != nil {
		return ResponseText(value)
	}
	return string(data)
}

func responseJSONValue(key string, value any) any {
	normalized := normalizeKey(key)
	switch typed := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for childKey := range typed {
			keys = append(keys, childKey)
		}
		sort.Strings(keys)
		result := make(map[string]any, len(typed))
		for _, childKey := range keys {
			result[childKey] = responseJSONValue(childKey, typed[childKey])
		}
		return result
	case map[string][]string:
		keys := make([]string, 0, len(typed))
		for childKey := range typed {
			keys = append(keys, childKey)
		}
		sort.Strings(keys)
		result := make(map[string]any, len(typed))
		for _, childKey := range keys {
			result[childKey] = responseJSONValue(childKey, typed[childKey])
		}
		return result
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			result[index] = responseJSONValue(key, item)
		}
		return result
	case []string:
		result := make([]any, len(typed))
		for index, item := range typed {
			result[index] = responseJSONValue(key, item)
		}
		return result
	case string:
		if isRedactedKey(normalized) {
			return redactedValue
		}
		if emailKeyNames[normalized] {
			return Email(typed)
		}
		if mobileKeyNames[normalized] {
			return Mobile(typed)
		}
		return InternalNetworkText(typed)
	default:
		if isRedactedKey(normalized) {
			return redactedValue
		}
		return typed
	}
}

// InternalNetworkText redacts private/loopback/link-local addresses and
// internal-only hostnames while retaining public business URLs with their
// sensitive query values masked.
func InternalNetworkText(value string) string {
	value = errorURLPattern.ReplaceAllStringFunc(value, func(candidate string) string {
		parsed, err := url.Parse(candidate)
		if err != nil {
			return candidate
		}
		if isInternalHost(parsed.Hostname()) {
			return "[INTERNAL_REDACTED]"
		}
		return URLString(candidate)
	})
	value = internalIPv4Pattern.ReplaceAllStringFunc(value, func(candidate string) string {
		host := candidate
		if parsedHost, _, err := net.SplitHostPort(candidate); err == nil {
			host = parsedHost
		} else if index := strings.LastIndex(candidate, ":"); index > 0 {
			host = candidate[:index]
		}
		if isInternalHost(host) {
			return "[INTERNAL_REDACTED]"
		}
		return candidate
	})
	value = internalIPv6Pattern.ReplaceAllStringFunc(value, func(candidate string) string {
		host := candidate
		if parsedHost, _, err := net.SplitHostPort(candidate); err == nil {
			host = parsedHost
		} else {
			host = strings.Trim(candidate, "[]")
		}
		// internalIPv6Pattern 的 `(?:[0-9a-f]{0,4}:){2,}[0-9a-f]{0,4}` 分支也会
		// 命中 URL 路径里的冒号数字片段（例如抖音图片尺寸
		// `~tplv-dy-aweme-images:300:300.jpeg`），而 isInternalHost 对任何不含
		// 点的字符串都返回 true（它假定入参已是主机名）。不校验字面量就会出现
		// 公网业务链接被改成 `[INTERNAL_REDACTED]` 的假阳性，因此这里要求
		// candidate 本身能被 net.ParseIP 解析成 IPv6 才继续判定。
		if ip := net.ParseIP(strings.Trim(host, "[]")); ip == nil || ip.To4() != nil {
			return candidate
		}
		if isInternalHost(host) {
			return "[INTERNAL_REDACTED]"
		}
		return candidate
	})
	return internalHostnamePattern.ReplaceAllString(value, "[INTERNAL_REDACTED]")
}

func URLString(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return value
	}
	changed := false
	if parsed.User != nil {
		parsed.User = url.User(redactedValue)
		changed = true
	}
	query := parsed.Query()
	for key, values := range query {
		normalized := normalizeKey(key)
		for index, item := range values {
			if isRedactedKey(normalized) {
				values[index] = redactedValue
				changed = true
				continue
			}
			if emailKeyNames[normalized] {
				values[index] = Email(item)
				changed = true
				continue
			}
			if mobileKeyNames[normalized] {
				values[index] = Mobile(item)
				changed = true
			}
		}
		query[key] = values
	}
	if !changed {
		return value
	}
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

// UpstreamReference returns a safe diagnostic reference for an upstream
// endpoint.  Upstream URLs are routing secrets: even a URL without credentials
// can disclose a provider, private host, path, query values, or an embedded
// user-supplied URL.  Call-log persistence and DTOs therefore keep only a
// stable redaction marker, never a partially masked URL.
func UpstreamReference(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if safeUpstreamReferencePattern.MatchString(value) {
		return value
	}
	return redactedEndpointValue
}

// UpstreamReferences records how many upstream candidates were attempted while
// keeping every candidate address private.  The node labels are deliberately
// synthetic and contain no host, path, query, port, or IP information.
func UpstreamReferences(values []string) string {
	refs := make([]string, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			continue
		}
		refs = append(refs, "node-"+strconv.Itoa(len(refs)+1))
	}
	return strings.Join(refs, ",")
}

func Error(err error) string {
	if err == nil {
		return ""
	}
	return ErrorText(err.Error())
}

// EndpointText removes network endpoints from arbitrary text while leaving
// non-network diagnostic content intact.  It is used for administrator log
// views where other fields may remain available for investigation, but no
// upstream hostname, path, query, port, or IP may be recovered.
func EndpointText(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	return redactErrorEndpoints(value)
}

// LogText is the persistence-safe text path for call-log fields.  It applies
// credential/privacy masking first and then removes all endpoint details.
func LogText(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	return ErrorText(Text(value))
}

// LogJSONText is the persistence-safe JSON path for call-log fields.  Invalid
// JSON is treated as text so a transport error or plain response cannot bypass
// endpoint masking.
func LogJSONText(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	return ErrorText(SensitiveJSONText(value))
}

func ErrorText(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	value = errorPEMPrivateKeyPattern.ReplaceAllString(value, redactedValue)
	value = errorMySQLDSNPattern.ReplaceAllStringFunc(value, redactEndpointCandidate)
	// Remove complete network endpoints before credential substitution. URL
	// query values may themselves contain the literal `[REDACTED]` marker after
	// a previous masking pass; processing credentials first would turn the
	// encoded `%5D` into a trailing `]` and make endpoint punctuation look like
	// part of the replacement (`[UPSTREAM_REDACTED]]`). Endpoint-first ordering
	// keeps the redaction marker stable while the remaining passes handle
	// credentials in non-network diagnostics.
	value = redactExplicitEndpoints(value)
	value = errorBearerPattern.ReplaceAllString(value, `${1}`+redactedValue)
	value = errorCookiePattern.ReplaceAllString(value, `${1}`+redactedValue)
	value = errorSensitiveDoublePattern.ReplaceAllString(value, `${1}`+redactedValue)
	value = errorSensitiveSinglePattern.ReplaceAllString(value, `${1}`+redactedValue)
	value = errorSensitivePairPattern.ReplaceAllString(value, `${1}`+redactedValue)
	value = errorEmailPattern.ReplaceAllStringFunc(value, Email)
	value = errorMobilePattern.ReplaceAllStringFunc(value, Mobile)
	return redactErrorEndpoints(value)
}

// StackText sanitizes a runtime stack before it is written to an application
// log. Stacks contain source paths and file names (for example
// "recovery_test.go") that must remain intact for diagnostics, so this helper
// only removes explicit URL/IP endpoint forms and does not apply the broad
// bare-domain pass used by ErrorText.
func StackText(value string) string {
	if strings.TrimSpace(value) == "" {
		return value
	}
	value = errorPEMPrivateKeyPattern.ReplaceAllString(value, redactedValue)
	value = errorMySQLDSNPattern.ReplaceAllStringFunc(value, redactEndpointCandidate)
	// Scrub complete endpoints before credential substitution for the same
	// reason as ErrorText: a token value may be rewritten to a literal
	// `[REDACTED]` inside a URL, and the closing bracket would otherwise be
	// retained as punctuation by redactEndpointCandidate.
	value = redactExplicitEndpoints(value)
	value = errorBearerPattern.ReplaceAllString(value, `${1}`+redactedValue)
	value = errorCookiePattern.ReplaceAllString(value, `${1}`+redactedValue)
	value = errorSensitiveDoublePattern.ReplaceAllString(value, `${1}`+redactedValue)
	value = errorSensitiveSinglePattern.ReplaceAllString(value, `${1}`+redactedValue)
	value = errorSensitivePairPattern.ReplaceAllString(value, `${1}`+redactedValue)
	value = errorEmailPattern.ReplaceAllStringFunc(value, Email)
	value = errorMobilePattern.ReplaceAllStringFunc(value, Mobile)
	value = redactExplicitEndpoints(value)
	value = redactIPv6Endpoints(value)
	return redactStackNetworkHosts(value)
}

func redactStackNetworkHosts(value string) string {
	lines := strings.Split(value, "\n")
	for lineIndex, line := range lines {
		matches := stackNetworkHostPattern.FindAllStringSubmatchIndex(line, -1)
		for index := len(matches) - 1; index >= 0; index-- {
			match := matches[index]
			if len(match) < 6 || match[4] < 0 || match[5] < 0 {
				continue
			}
			hostStart, hostEnd := match[4], match[5]
			line = line[:hostStart] + redactEndpointCandidate(line[hostStart:hostEnd]) + line[hostEnd:]
		}
		// A DNS resolver may omit the host's port and place a qualifier such as
		// "on" or "at" after it. Replace only the host capture, preserving the
		// qualifier and the rest of the diagnostic text.
		lookupMatches := stackSingleLabelLookupPattern.FindAllStringSubmatchIndex(line, -1)
		for index := len(lookupMatches) - 1; index >= 0; index-- {
			match := lookupMatches[index]
			if len(match) < 6 || match[4] < 0 || match[5] < 0 {
				continue
			}
			hostStart, hostEnd := match[4], match[5]
			line = line[:hostStart] + redactEndpointCandidate(line[hostStart:hostEnd]) + line[hostEnd:]
		}
		lines[lineIndex] = line
	}
	return strings.Join(lines, "\n")
}

// redactErrorEndpoints removes network endpoints from diagnostic text. An
// error returned by net/http commonly includes the complete request URL (and
// sometimes a percent-encoded URL nested in a query parameter). Masking only
// credentials is insufficient because the host, route, and user-supplied URL
// can still disclose an upstream integration. Keep the timeout/operation text
// around the endpoint so operators retain useful context without exposing the
// destination.
func redactErrorEndpoints(value string) string {
	value = redactExplicitEndpoints(value)
	value = redactIPv6Endpoints(value)

	// A few transports format an endpoint without a scheme. Redact domain-like
	// host/path tokens as a final pass, while preserving the domain portion of a
	// previously masked email address (the match starts immediately after '@').
	matches := errorBareEndpointPattern.FindAllStringIndex(value, -1)
	for index := len(matches) - 1; index >= 0; index-- {
		start, end := matches[index][0], matches[index][1]
		if start > 0 && value[start-1] == '@' {
			continue
		}
		value = value[:start] + redactedEndpointValue + value[end:]
	}
	// Apply the single-label service pass after dotted hosts and IPv6 have been
	// handled.  Otherwise a suffix such as `example/private` in
	// `secret.example/private` could be replaced first, leaving `secret.` (or a
	// fragment of an IPv6 address) visible.
	value = redactSingleLabelEndpoints(value)
	// DNS/transport errors can omit a port (`lookup localhost: no such host` or
	// `dial tcp api-backend: no such host`).  Reuse the context-aware stack
	// matcher here so those bare service names are removed without treating
	// ordinary prose elsewhere in the message as a host.
	return redactStackNetworkHosts(value)
}

func redactExplicitEndpoints(value string) string {
	// Run the broad encoded-token pass first.  It subsumes normal HTTP(S)
	// URLs as well as repeated percent encoding and JSON-unicode escaped
	// delimiters, preventing later narrower passes from leaving an encoded path
	// or nested URL fragment behind.
	value = errorEncodedEndpointPattern.ReplaceAllStringFunc(value, redactEndpointCandidate)
	value = errorEscapedURLPattern.ReplaceAllStringFunc(value, redactEndpointCandidate)
	value = errorURLPattern.ReplaceAllStringFunc(value, redactEndpointCandidate)
	value = errorEncodedURLPattern.ReplaceAllStringFunc(value, redactEndpointCandidate)
	value = errorIPv4EndpointPattern.ReplaceAllStringFunc(value, redactEndpointCandidate)
	value = errorIPv6EndpointPattern.ReplaceAllStringFunc(value, redactEndpointCandidate)
	return value
}

func redactSingleLabelEndpoints(value string) string {
	return errorSingleLabelEndpointPattern.ReplaceAllStringFunc(value, redactEndpointCandidate)
}

// redactIPv6Endpoints validates colon-heavy tokens before replacing them.
// Regex-only IPv6 matching tends to either miss compressed addresses or eat
// timestamps.  Keep the candidate boundaries broad, then require a valid
// net.ParseIP result after removing an optional path and zone identifier.
func redactIPv6Endpoints(value string) string {
	matches := looseIPv6TokenPattern.FindAllStringIndex(value, -1)
	for index := len(matches) - 1; index >= 0; index-- {
		start, end := matches[index][0], matches[index][1]
		candidate := value[start:end]
		suffix := ""
		for len(candidate) > 0 {
			last := candidate[len(candidate)-1]
			if !strings.ContainsRune(".,;:!?)]}", rune(last)) {
				break
			}
			suffix = string(last) + suffix
			candidate = candidate[:len(candidate)-1]
		}
		if candidate == "" {
			continue
		}
		host := candidate
		if slash := strings.IndexByte(host, '/'); slash >= 0 {
			host = host[:slash]
		}
		if zone := strings.LastIndexByte(host, '%'); zone >= 0 {
			host = host[:zone]
		}
		if strings.Count(host, ":") < 2 || net.ParseIP(host) == nil {
			continue
		}
		value = value[:start] + redactedEndpointValue + suffix + value[end:]
	}
	return value
}

func redactEndpointCandidate(value string) string {
	if value == "" {
		return redactedEndpointValue
	}
	// Keep sentence punctuation that the URL regexp intentionally captures at
	// the end of a quoted/error token.
	suffix := ""
	for len(value) > 0 {
		last := value[len(value)-1]
		if !strings.ContainsRune(".,;:!?)]}", rune(last)) {
			break
		}
		suffix = string(last) + suffix
		value = value[:len(value)-1]
	}
	return redactedEndpointValue + suffix
}

func Text(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
		return URLString(trimmed)
	}
	return SensitiveJSONText(value)
}

func sensitiveJSONValue(key string, value any) any {
	normalized := normalizeKey(key)
	switch typed := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for childKey := range typed {
			keys = append(keys, childKey)
		}
		sort.Strings(keys)
		result := make(map[string]any, len(typed))
		for _, childKey := range keys {
			result[childKey] = sensitiveJSONValue(childKey, typed[childKey])
		}
		return result
	case map[string][]string:
		keys := make([]string, 0, len(typed))
		for childKey := range typed {
			keys = append(keys, childKey)
		}
		sort.Strings(keys)
		result := make(map[string]any, len(typed))
		for _, childKey := range keys {
			result[childKey] = sensitiveJSONValue(childKey, typed[childKey])
		}
		return result
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			result[index] = sensitiveJSONValue(key, item)
		}
		return result
	case []string:
		result := make([]any, len(typed))
		for index, item := range typed {
			result[index] = sensitiveJSONValue(key, item)
		}
		return result
	case string:
		if isRedactedKey(normalized) {
			return redactedValue
		}
		if emailKeyNames[normalized] {
			return Email(typed)
		}
		if mobileKeyNames[normalized] {
			return Mobile(typed)
		}
		return typed
	default:
		if isRedactedKey(normalized) {
			return redactedValue
		}
		return typed
	}
}

func userVisibleJSONValue(key string, value any) any {
	switch typed := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for childKey := range typed {
			keys = append(keys, childKey)
		}
		sort.Strings(keys)
		result := make(map[string]any, len(typed))
		for _, childKey := range keys {
			if internalRoutingKeyNames[normalizeKey(childKey)] {
				continue
			}
			result[childKey] = userVisibleJSONValue(childKey, typed[childKey])
		}
		return result
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			result[index] = userVisibleJSONValue(key, item)
		}
		return result
	case string:
		return InternalNetworkText(typed)
	default:
		return typed
	}
}

func isInternalHost(value string) bool {
	host := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(value)), ".")
	if host == "" {
		return false
	}
	if ip := net.ParseIP(strings.Trim(host, "[]")); ip != nil {
		return ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() ||
			ip.IsLinkLocalMulticast() || ip.IsUnspecified()
	}
	if host == "localhost" || !strings.Contains(host, ".") {
		return true
	}
	for _, suffix := range []string{
		".cluster.local", ".internal", ".lan", ".local", ".localhost", ".svc",
	} {
		if strings.HasSuffix(host, suffix) {
			return true
		}
	}
	return false
}

func normalizeKey(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	replacer := strings.NewReplacer("_", "", "-", "", ".", "", " ", "")
	return replacer.Replace(value)
}

func isRedactedKey(normalized string) bool {
	if redactedKeyNames[normalized] {
		return true
	}
	return strings.HasSuffix(normalized, "token") ||
		strings.HasSuffix(normalized, "password") ||
		strings.HasSuffix(normalized, "secret") ||
		strings.HasSuffix(normalized, "privatekey")
}

func middle(value string, keepStart, keepEnd int) string {
	runes := []rune(value)
	if len(runes) <= keepStart+keepEnd {
		if len(runes) <= 1 {
			return "*"
		}
		return string(runes[:1]) + "***"
	}
	return string(runes[:keepStart]) + "****" + string(runes[len(runes)-keepEnd:])
}

func firstRunes(value string, count int) string {
	runes := []rune(value)
	if len(runes) <= count {
		return value
	}
	return string(runes[:count])
}

func lastRunes(value string, count int) string {
	runes := []rune(value)
	if len(runes) <= count {
		return value
	}
	return string(runes[len(runes)-count:])
}
