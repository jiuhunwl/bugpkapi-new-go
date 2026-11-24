// Package phoneutil 提供手机号（E.164）归一化、国家码归属与脱敏。
package phoneutil

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// CountryCodeCN 是中国大陆国家码，注册页默认值与运营号段判断都用它。
	CountryCodeCN = "86"

	minE164Digits = 8
	maxE164Digits = 15
	maxInputLen   = 40

	// maskStars 固定长度，避免脱敏结果把号码长度也透露出去。
	maskStars = "****"
)

var (
	// ErrEmpty 表示输入为空。
	ErrEmpty = errors.New("phoneutil: empty input")
	// ErrInvalid 表示去分隔符后不是合法号码。
	ErrInvalid = errors.New("phoneutil: invalid phone number")
	// ErrTooLong 表示原始输入超出合理长度，通常来自误粘贴或脚本注入。
	ErrTooLong = errors.New("phoneutil: phone input too long")
	// ErrNeedCountryCode 表示号码没有国际前缀，且调用方没有给出默认国家码。
	ErrNeedCountryCode = errors.New("phoneutil: country code is required")
)

// Normalize 把用户输入转成严格 E.164（"+" 开头、8~15 位数字）。
//
// 接受空格、连字符、括号、点号与全角数字，也接受 "00" 开头的国际前缀。
// 没有前缀的裸号码不会被猜测归属国，返回 ErrNeedCountryCode——猜错国家码
// 等于把短信发到另一个国家并按错档位计费，必须由调用方显式指定。
func Normalize(raw string) (string, error) {
	return NormalizeWithDefault(raw, "")
}

// NormalizeWithDefault 在 Normalize 的基础上，允许为裸号码补 defaultCountryCode。
// defaultCountryCode 必须是 1~3 位、不以 0 开头的数字（可带 "+"）。传 "" 等价于 Normalize。
func NormalizeWithDefault(raw, defaultCountryCode string) (string, error) {
	value, hasIntlPrefix, err := scan(raw)
	if err != nil {
		return "", err
	}
	if hasIntlPrefix {
		return finalize(value)
	}
	code := ParseCountryCode(defaultCountryCode)
	if code == "" {
		return "", ErrNeedCountryCode
	}
	if err := validateCountryCode(code); err != nil {
		return "", err
	}
	return finalize(code + value)
}

// NormalizeMainland 归一化中国大陆手机号，接受 13800138000、8613800138000、
// +86 138-0013-8000、008613800138000 等写法，只接受 +86 且号段合法的结果。
func NormalizeMainland(raw string) (string, error) {
	value, err := NormalizeWithDefault(raw, CountryCodeCN)
	if err == nil && IsMainlandMobile(value) {
		return value, nil
	}
	// "86"+11 位是国内用户最常见的粘贴写法，此时再补默认国家码会变成 +8686……
	// 只在长度与号段都能对上时才认定号码自带前缀，不做一般性的前缀猜测。
	digits, hasIntlPrefix, scanErr := scan(raw)
	if scanErr == nil && !hasIntlPrefix &&
		len(digits) == len(CountryCodeCN)+11 && strings.HasPrefix(digits, CountryCodeCN) {
		if alt, altErr := finalize(digits); altErr == nil && IsMainlandMobile(alt) {
			return alt, nil
		}
	}
	if err != nil {
		return "", err
	}
	return "", ErrInvalid
}

// IsValid 报告 value 是否是严格 E.164。
func IsValid(value string) bool {
	trimmed := strings.TrimSpace(value)
	if !strings.HasPrefix(trimmed, "+") {
		return false
	}
	_, err := finalize(strings.TrimPrefix(trimmed, "+"))
	return err == nil
}

// Digits 返回已归一化号码 "+" 之后的数字段；输入不是合法 E.164 时返回空串。
func Digits(value string) string {
	trimmed := strings.TrimSpace(value)
	if !strings.HasPrefix(trimmed, "+") {
		return ""
	}
	digits := strings.TrimPrefix(trimmed, "+")
	if _, err := finalize(digits); err != nil {
		return ""
	}
	return digits
}

// CountryCode 返回号码的国家/地区码（最长匹配）。表里没有的号段或号码非法时
// known 为 false，调用方应默认拒绝：归因错误会把高价国家算进低价预算桶。
func CountryCode(value string) (code string, known bool) {
	digits := Digits(value)
	if digits == "" {
		return "", false
	}
	return lookupCountryCode(digits)
}

// ParseCountryCode 把 "+86" / "86" 归一成 "86"。
func ParseCountryCode(code string) string {
	return strings.TrimPrefix(strings.TrimSpace(code), "+")
}

// IsMainlandMobile 报告号码是否为中国大陆手机号（+86 且 11 位、1[3-9] 开头）。
func IsMainlandMobile(value string) bool {
	digits := Digits(value)
	if !strings.HasPrefix(digits, CountryCodeCN) || len(digits) != len(CountryCodeCN)+11 {
		return false
	}
	subscriber := digits[len(CountryCodeCN):]
	if subscriber[0] != '1' {
		return false
	}
	switch subscriber[1] {
	case '3', '4', '5', '6', '7', '8', '9':
		return true
	default:
		return false
	}
}

// Mask 输出可展示、可写日志的脱敏号码，例如 +8613800138000 -> +8613****00。
// 保留国家码与订阅号首尾各两位，中段固定 4 个星号；号码非法时统一返回 "****"。
func Mask(value string) string {
	digits := Digits(value)
	if digits == "" {
		return maskStars
	}
	code, known := lookupCountryCode(digits)
	if !known || len(digits)-len(code) < 5 {
		return maskStars
	}
	subscriber := digits[len(code):]
	return "+" + code + subscriber[:2] + maskStars + subscriber[len(subscriber)-2:]
}

// scan 去分隔符并返回纯数字段。输入带 "+" 或 "00" 前缀时 hasIntlPrefix 为 true，
// 表示号码自带国际前缀，返回值已剥掉前缀。
func scan(raw string) (digits string, hasIntlPrefix bool, err error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", false, ErrEmpty
	}
	if utf8.RuneCountInString(trimmed) > maxInputLen {
		return "", false, ErrTooLong
	}
	// 先做宽度归一：中文输入法下的全角数字、加号、括号非常常见，
	// 放在前缀判断之前，否则 "＋８６…" 会被当成没有国际前缀的裸号码。
	// 全角空格要等归一后才能被 TrimSpace 看到，所以这里再削一次。
	normalized := strings.TrimSpace(normalizeWidth(trimmed))
	if normalized == "" {
		return "", false, ErrEmpty
	}
	if err := rejectLetters(normalized); err != nil {
		return "", false, err
	}

	var builder strings.Builder
	builder.Grow(len(normalized))
	for _, r := range normalized {
		switch {
		case r >= '0' && r <= '9':
			builder.WriteRune(r)
		case isIgnorable(r):
		default:
			return "", false, ErrInvalid
		}
	}

	value := builder.String()
	switch {
	case strings.HasPrefix(normalized, "+"):
		hasIntlPrefix = true
	case strings.HasPrefix(normalized, "00"):
		hasIntlPrefix = true
		value = strings.TrimPrefix(value, "00")
	}
	if value == "" {
		return "", false, ErrInvalid
	}
	return value, hasIntlPrefix, nil
}

// normalizeWidth 把全角数字、加号、括号与若干不间断空格折算成半角。
func normalizeWidth(value string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 0xFF10 && r <= 0xFF19:
			return '0' + (r - 0xFF10)
		case r == 0xFF0B:
			return '+'
		case r == 0xFF08:
			return '('
		case r == 0xFF09:
			return ')'
		case r == 0x3000, r == 0xA0:
			return ' '
		case r == 0x2010, r == 0x2011:
			return '-'
		default:
			return r
		}
	}, value)
}

// rejectLetters 拦掉字母号码（如 1-800-FLOWERS）：短信通道一律按数字路由，
// 把字母交给上游只会在运营商侧报错并白烧一次发送配额。
func rejectLetters(value string) error {
	for _, r := range value {
		if unicode.IsLetter(r) {
			return ErrInvalid
		}
	}
	return nil
}

func isIgnorable(r rune) bool {
	switch r {
	case '+', '-', '.', ' ', '\t', '(', ')', '（', '）', '/':
		return true
	default:
		return false
	}
}

// finalize 校验数字段并加上 "+"。
func finalize(digits string) (string, error) {
	if len(digits) < minE164Digits || len(digits) > maxE164Digits {
		return "", fmt.Errorf("%w: length %d", ErrInvalid, len(digits))
	}
	// ITU-T E.164：国家码首位不为 0。
	if digits[0] == '0' {
		return "", ErrInvalid
	}
	for i := 0; i < len(digits); i++ {
		if digits[i] < '0' || digits[i] > '9' {
			return "", ErrInvalid
		}
	}
	return "+" + digits, nil
}

func validateCountryCode(code string) error {
	if len(code) < 1 || len(code) > 3 || code[0] == '0' {
		return ErrInvalid
	}
	for i := 0; i < len(code); i++ {
		if code[i] < '0' || code[i] > '9' {
			return ErrInvalid
		}
	}
	return nil
}
