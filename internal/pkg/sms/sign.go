package sms

import (
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"sort"
	"strings"
	"time"
)

// percentEncode 按 RFC 3986 编码：不转义 A-Za-z0-9-_.~，其余一律 %XX（大写）。
//
// 阿里云 RPC 签名要求的就是这个口径。这里不用 url.QueryEscape，因为 Go 的
// QueryEscape 对 ~ 与 * 的处理和 RFC 3986 不一致，签名会随机性地在部分号码
// 上验签失败——这类问题在线上极难定位，必须在编码函数本身对齐规范。
func percentEncode(value string) string {
	var builder strings.Builder
	builder.Grow(len(value))
	for i := 0; i < len(value); i++ {
		c := value[i]
		if isUnreserved(c) {
			builder.WriteByte(c)
			continue
		}
		const hexDigits = "0123456789ABCDEF"
		builder.WriteByte('%')
		builder.WriteByte(hexDigits[c>>4])
		builder.WriteByte(hexDigits[c&0x0F])
	}
	return builder.String()
}

func isUnreserved(c byte) bool {
	switch {
	case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		return true
	case c == '-', c == '_', c == '.', c == '~':
		return true
	default:
		return false
	}
}

// canonicalAliyunQuery 生成规范化查询串（不含 Signature）。
func canonicalAliyunQuery(params map[string]string) string {
	keys := sortedParamKeys(params)
	pairs := make([]string, 0, len(keys))
	for _, key := range keys {
		pairs = append(pairs, percentEncode(key)+"="+percentEncode(params[key]))
	}
	return strings.Join(pairs, "&")
}

// aliyunStringToSign 组装待签名串：METHOD&percentEncode("/")&percentEncode(canonicalQuery)。
func aliyunStringToSign(method string, params map[string]string) string {
	return strings.ToUpper(method) + "&" + percentEncode("/") + "&" +
		percentEncode(canonicalAliyunQuery(params))
}

// aliyunSignature 用 AccessKeySecret+"&" 作密钥做 HMAC-SHA1 并 Base64。
func aliyunSignature(accessKeySecret, stringToSign string) string {
	mac := hmac.New(sha1.New, []byte(accessKeySecret+"&"))
	mac.Write([]byte(stringToSign))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

const (
	tencentAlgorithm = "TC3-HMAC-SHA256"
	tencentService   = "sms"
	tencentVersion   = "2021-01-11"
	tencentAction    = "SendSms"
)

// tencentCredentialScope 形如 2026-11-04/sms/tc3_request。
func tencentCredentialScope(date string) string {
	return date + "/" + tencentService + "/tc3_request"
}

// tencentCanonicalRequest 组装规范请求。头部名与小写值都要参与，顺序按头名升序。
func tencentCanonicalRequest(method, uri, query string, headers map[string]string, payload []byte) string {
	names := make([]string, 0, len(headers))
	for name := range headers {
		names = append(names, strings.ToLower(name))
	}
	names = sortedUnique(names)
	var builder strings.Builder
	builder.WriteString(strings.ToUpper(method))
	builder.WriteByte('\n')
	builder.WriteString(uri)
	builder.WriteByte('\n')
	builder.WriteString(query)
	builder.WriteByte('\n')
	for _, name := range names {
		builder.WriteString(name)
		builder.WriteByte(':')
		builder.WriteString(strings.TrimSpace(headers[name]))
		builder.WriteByte('\n')
	}
	builder.WriteByte('\n')
	builder.WriteString(strings.Join(names, ";"))
	builder.WriteByte('\n')
	builder.WriteString(hexSHA256(payload))
	return builder.String()
}

// tencentStringToSign 组装待签名串。RequestTimestamp 必须是 UTC 的 ISO8601
// 秒级格式（2026-11-04T08:30:00Z），不是 X-TC-Timestamp 头里的 Unix 秒。
func tencentStringToSign(timestamp time.Time, canonicalRequest string) string {
	date := timestamp.UTC().Format("2006-01-02")
	var builder strings.Builder
	builder.WriteString(tencentAlgorithm)
	builder.WriteByte('\n')
	builder.WriteString(timestamp.UTC().Format(timestampLayout))
	builder.WriteByte('\n')
	builder.WriteString(tencentCredentialScope(date))
	builder.WriteByte('\n')
	builder.WriteString(hexSHA256([]byte(canonicalRequest)))
	return builder.String()
}

// tencentSignature 派生密钥链：TC3+SecretKey -> date -> service -> tc3_request。
func tencentSignature(accessKeySecret, date, stringToSign string) string {
	secretDate := hmacSHA256([]byte("TC3"+accessKeySecret), date)
	secretService := hmacSHA256(secretDate, tencentService)
	secretSigning := hmacSHA256(secretService, "tc3_request")
	return hex.EncodeToString(hmacSHA256(secretSigning, stringToSign))
}

// tencentAuthorization 拼出 Authorization 头。
func tencentAuthorization(accessKeyID, date, signedHeaders, signature string) string {
	return tencentAlgorithm + " " +
		"Credential=" + accessKeyID + "/" + tencentCredentialScope(date) + ", " +
		"SignedHeaders=" + signedHeaders + ", " +
		"Signature=" + signature
}

func hmacSHA256(key []byte, data string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(data))
	return mac.Sum(nil)
}

func hexSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func sortedUnique(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	// map 遍历顺序不定，签名串必须逐字节稳定，所以显式排序。
	sort.Strings(out)
	return out
}

// timestampLayout 是阿里云 RPC Timestamp 参数要求的 UTC ISO8601 格式。
const timestampLayout = "2006-01-02T15:04:05Z"
