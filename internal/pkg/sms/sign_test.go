package sms

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestPercentEncode(t *testing.T) {
	// 这几条是 RFC 3986 / 阿里云 RPC percentEncode 的规范事实，不是实现自证。
	tests := []struct{ in, want string }{
		{in: "abc", want: "abc"},
		{in: "A-Z_a.z~0", want: "A-Z_a.z~0"},
		{in: " ", want: "%20"},
		{in: "*", want: "%2A"},
		{in: "/", want: "%2F"},
		{in: "+", want: "%2B"},
		{in: "=", want: "%3D"},
		{in: "&", want: "%26"},
		{in: ":", want: "%3A"},
		{in: "%", want: "%25"},
		{in: "中文", want: "%E4%B8%AD%E6%96%87"},
	}
	for _, tt := range tests {
		if got := percentEncode(tt.in); got != tt.want {
			t.Fatalf("percentEncode(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestCanonicalAliyunQueryIsStable(t *testing.T) {
	params := map[string]string{
		"Action":       "SendSms",
		"SignName":     "BugPk",
		"Timestamp":    "2026-11-04T00:00:00Z",
		"PhoneNumbers": "+8613800138000",
		"A":            "1",
	}
	want := "A=1&Action=SendSms&PhoneNumbers=%2B8613800138000&SignName=BugPk&Timestamp=2026-11-04T00%3A00%3A00Z"
	// map 遍历顺序不定，重复跑才暴露得出漏排序的问题。
	for i := 0; i < 50; i++ {
		if got := canonicalAliyunQuery(params); got != want {
			t.Fatalf("canonicalAliyunQuery = %q, want %q", got, want)
		}
	}
}

func TestAliyunStringToSign(t *testing.T) {
	params := map[string]string{"A": "1", "B": "2"}
	got := aliyunStringToSign(http.MethodGet, params)
	want := "GET&%2F&A%3D1%26B%3D2"
	if got != want {
		t.Fatalf("aliyunStringToSign = %q, want %q", got, want)
	}
	if got := aliyunStringToSign("post", params); !strings.HasPrefix(got, "POST&") {
		t.Fatalf("method must be uppercased, got %q", got)
	}
}

func TestAliyunSignatureIsDeterministic(t *testing.T) {
	const secret = "test-secret"
	stringToSign := "GET&%2F&A%3D1"
	first := aliyunSignature(secret, stringToSign)
	second := aliyunSignature(secret, stringToSign)
	if first != second {
		t.Fatalf("signature is not deterministic: %q vs %q", first, second)
	}
	if first == aliyunSignature("other-secret", stringToSign) {
		t.Fatal("signature must depend on the access key secret")
	}
	if strings.Contains(first, "=") && !strings.HasSuffix(first, "=") {
		// Base64 的填充只可能出现在末尾。
		t.Fatalf("signature is not standard base64: %q", first)
	}
	if len(first) == 0 {
		t.Fatal("empty signature")
	}
}

func TestTencentCanonicalRequestLayout(t *testing.T) {
	headers := map[string]string{
		"content-type": tencentContentType,
		"host":         "sms.tencentcloudapi.com",
		"x-tc-action":  tencentAction,
	}
	payload := []byte(`{"a":1}`)
	got := tencentCanonicalRequest(http.MethodPost, "/", "", headers, payload)
	lines := strings.Split(got, "\n")
	// 规范请求的六个字段里 CanonicalHeaders 自带行尾换行，所以头部段之后还有
	// 一个空行作分隔：method/URI/query + 3 个头部 + 空行 + SignedHeaders + payload hash。
	if len(lines) != 9 {
		t.Fatalf("canonical request must have 9 lines, got %d: %q", len(lines), got)
	}
	if lines[0] != "POST" || lines[1] != "/" || lines[2] != "" {
		t.Fatalf("unexpected first lines: %q", lines[:3])
	}
	if lines[3] != "content-type:"+tencentContentType ||
		lines[4] != "host:sms.tencentcloudapi.com" ||
		lines[5] != "x-tc-action:SendSms" {
		t.Fatalf("headers must be sorted with original-case values: %q", lines[3:6])
	}
	if lines[6] != "" || lines[7] != "content-type;host;x-tc-action" {
		t.Fatalf("signed header section = %q", lines[6:8])
	}
	// 官方格式的最后一行是 payload 的 sha256 hex。期望值由 coreutils
	// `printf '{"a":1}' | sha256sum` 独立算出，不是实现自证。
	if want := "015abd7f5cc57a2dd94b7590f04ad8084273905ee33ec5cebeae62276a97f862"; lines[len(lines)-1] != want {
		t.Fatalf("hashed payload = %q, want %q", lines[len(lines)-1], want)
	}
}

func TestTencentStringToSignAndAuthorization(t *testing.T) {
	now := time.Date(2026, 11, 4, 8, 30, 0, 0, time.UTC)
	stringToSign := tencentStringToSign(now, "canonical")
	lines := strings.Split(stringToSign, "\n")
	if len(lines) != 4 {
		t.Fatalf("string to sign must have 4 lines, got %d", len(lines))
	}
	if lines[0] != tencentAlgorithm {
		t.Fatalf("algorithm line = %q", lines[0])
	}
	if lines[1] != "2026-11-04T08:30:00Z" {
		t.Fatalf("timestamp line = %q, want ISO8601 UTC seconds", lines[1])
	}
	if lines[2] != "2026-11-04/sms/tc3_request" {
		t.Fatalf("credential scope = %q", lines[2])
	}
	if len(lines[3]) != 64 {
		t.Fatalf("hashed canonical request must be sha256 hex, got %q", lines[3])
	}

	signature := tencentSignature("secret", "2026-11-04", stringToSign)
	if len(signature) != 64 {
		t.Fatalf("signature must be 64 hex chars, got %d", len(signature))
	}
	if signature == tencentSignature("other", "2026-11-04", stringToSign) {
		t.Fatal("signature must depend on the secret")
	}
	authorization := tencentAuthorization("AKIDtest", "2026-11-04", "content-type;host;x-tc-action", signature)
	want := "TC3-HMAC-SHA256 Credential=AKIDtest/2026-11-04/sms/tc3_request, " +
		"SignedHeaders=content-type;host;x-tc-action, Signature=" + signature
	if authorization != want {
		t.Fatalf("Authorization = %q, want %q", authorization, want)
	}
}

func TestTimestampLayoutMatchesVendorDocs(t *testing.T) {
	now := time.Date(2026, 11, 4, 8, 30, 0, 0, time.UTC)
	// 阿里云 RPC 的 Timestamp 参数与腾讯云 TC3 的 RequestTimestamp 用的是同一种
	// ISO8601 UTC 秒级格式；带上本地时区偏移就必然验签失败。
	if got := now.UTC().Format(timestampLayout); got != "2026-11-04T08:30:00Z" {
		t.Fatalf("timestampLayout = %q", got)
	}
	local := time.FixedZone("CST", 8*3600)
	if got := now.In(local).UTC().Format(timestampLayout); got != "2026-11-04T08:30:00Z" {
		t.Fatalf("timestamp must be timezone independent, got %q", got)
	}
}

func TestHexSHA256AgainstIndependentVector(t *testing.T) {
	// 期望值来自 coreutils `printf '' | sha256sum`。
	if got := hexSHA256(nil); got != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Fatalf("hexSHA256(nil) = %q", got)
	}
}
