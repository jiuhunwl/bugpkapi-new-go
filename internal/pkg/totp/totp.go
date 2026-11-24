// Package totp 实现 RFC 6238 TOTP（time-based one-time password），
// 只依赖标准库：HMAC-SHA1、Base32。参数与主流认证器
// （Google Authenticator / Microsoft Authenticator / 1Password）默认值对齐：
// SHA1 摘要、6 位数字、30 秒步长、允许前后各 1 个窗口的时钟漂移。
package totp

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	// Step 是 TOTP 的时间步长。
	Step = 30 * time.Second
	// Digits 是验证码位数。
	Digits = 6
	// Skew 是允许的窗口漂移：验证时同时接受 t-1 / t / t+1 三个窗口。
	Skew = 1
	// SecretSize 是密钥字节数（160 位，与 HMAC-SHA1 摘要长度一致）。
	SecretSize = 20
)

// codeLen 固定 6 位输出。
const codeLen = 6

var encoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// GenerateSecret 生成新的 Base32 密钥。20 字节随机数，RFC 4226 推荐
// 最小 160 位；返回值不带填充，可直接写进 otpauth://secret= 参数。
func GenerateSecret() (string, error) {
	raw := make([]byte, SecretSize)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("totp: generate secret: %w", err)
	}
	return encoding.EncodeToString(raw), nil
}

// ValidateSecret 检查 Base32 密钥是否可解码（非空且长度合理）。
func ValidateSecret(secret string) bool {
	normalized := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(secret), " ", ""))
	if normalized == "" {
		return false
	}
	raw, err := encoding.DecodeString(normalized)
	if err != nil || len(raw) < 10 {
		return false
	}
	return true
}

// Code 计算给定时刻的 6 位验证码。
func Code(secret string, t time.Time) (string, error) {
	return codeAt(secret, counter(t))
}

// Verify 按标准窗口校验验证码：接受 t-1 / t / t+1 三个窗口中的任意一个，
// 吸收手机与服务端之间 ≤90 秒的时钟漂移。内部使用 constant time 比较。
func Verify(secret, code string, t time.Time) (bool, error) {
	return VerifyWithDrift(secret, code, t, Skew)
}

// VerifyWithDrift 自定义窗口漂移的校验入口，供测试与特殊场景使用。
func VerifyWithDrift(secret, code string, t time.Time, drift int64) (bool, error) {
	normalized := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(secret), " ", ""))
	key, err := encoding.DecodeString(normalized)
	if err != nil {
		return false, fmt.Errorf("totp: decode secret: %w", err)
	}
	given := strings.TrimSpace(code)
	if len(given) != codeLen {
		return false, nil
	}
	// 数字码不会包含空格；带非数字字符直接拒绝，防止宽松解码路径。
	for _, r := range given {
		if r < '0' || r > '9' {
			return false, nil
		}
	}
	for _, d := range driftWindows(drift) {
		want, err := hotp(key, counter(t)+d)
		if err != nil {
			return false, err
		}
		if subtle.ConstantTimeCompare([]byte(want), []byte(given)) == 1 {
			return true, nil
		}
	}
	return false, nil
}

// ProvisioningURL 构造 otpauth://totp/... 绑定链接。issuer 同时写入
// 路径前缀与 issuer 参数（双写是 Google Authenticator 的兼容要求）。
func ProvisioningURL(secret, issuer, account string) string {
	normalized := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(secret), " ", ""))
	label := url.PathEscape(issuer)
	if account != "" {
		label = url.PathEscape(issuer + ":" + account)
	}
	params := url.Values{}
	params.Set("secret", normalized)
	if issuer != "" {
		params.Set("issuer", issuer)
	}
	if account != "" {
		params.Set("account", account)
	}
	return "otpauth://totp/" + label + "?" + params.Encode()
}

func driftWindows(drift int64) []int64 {
	if drift < 0 {
		drift = 0
	}
	out := make([]int64, 0, 2*drift+1)
	for d := -drift; d <= drift; d++ {
		out = append(out, d)
	}
	return out
}

func counter(t time.Time) int64 {
	return t.Unix() / int64(Step/time.Second)
}

func codeAt(secret string, counter int64) (string, error) {
	normalized := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(secret), " ", ""))
	key, err := encoding.DecodeString(normalized)
	if err != nil {
		return "", fmt.Errorf("totp: decode secret: %w", err)
	}
	return hotp(key, counter)
}

// hotp 是 RFC 4226 的截断算法：HMAC-SHA1 -> dynamic truncation -> mod 10^6。
func hotp(key []byte, counter int64) (string, error) {
	mac := hmac.New(sha1.New, key)
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], uint64(counter))
	if _, err := mac.Write(buf[:]); err != nil {
		return "", fmt.Errorf("totp: hmac: %w", err)
	}
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	value := (uint32(sum[offset])&0x7f)<<24 |
		uint32(sum[offset+1])<<16 |
		uint32(sum[offset+2])<<8 |
		uint32(sum[offset+3])
	text := strconv.FormatUint(uint64(value%1_000_000), 10)
	// 前导零补齐到固定 6 位。
	for len(text) < codeLen {
		text = "0" + text
	}
	return text, nil
}
