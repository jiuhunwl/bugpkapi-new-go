package phoneutil

import (
	"errors"
	"strings"
	"testing"
)

func TestNormalizeWithDefault(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		def     string
		want    string
		wantErr error
	}{
		// 回归："+" 不进数字缓冲，早期版本在这里多切了一位，把 +8613… 变成 +613…
		{name: "plus prefix keeps first digit", raw: "+8613800138000", want: "+8613800138000"},
		{name: "plus with spaces", raw: " +86 138 0013 8000 ", want: "+8613800138000"},
		{name: "plus with dashes", raw: "+86-138-0013-8000", want: "+8613800138000"},
		{name: "plus with parens", raw: "+86 (138) 0013-8000", want: "+8613800138000"},
		{name: "full width digits", raw: "＋８６１３８００１３８０００", want: "+8613800138000"},
		{name: "double zero prefix", raw: "008613800138000", want: "+8613800138000"},
		{name: "double zero with dashes", raw: "00-86-138-0013-8000", want: "+8613800138000"},
		{name: "bare needs country code", raw: "13800138000", wantErr: ErrNeedCountryCode},
		{name: "bare with default", raw: "13800138000", def: "86", want: "+8613800138000"},
		{name: "default accepts plus form", raw: "13800138000", def: "+86", want: "+8613800138000"},
		{name: "default ignored when intl given", raw: "+14155552671", def: "86", want: "+14155552671"},
		{name: "empty", raw: "", wantErr: ErrEmpty},
		{name: "blank only", raw: "   ", wantErr: ErrEmpty},
		{name: "letters rejected", raw: "+1-800-FLOWERS", wantErr: ErrInvalid},
		{name: "too short", raw: "+861000", wantErr: ErrInvalid},
		{name: "too long", raw: "+86138001380001234", wantErr: ErrInvalid},
		{name: "leading zero rejected", raw: "+0861380013800", wantErr: ErrInvalid},
		{name: "input too long", raw: "+" + strings.Repeat("1", 60), wantErr: ErrTooLong},
		{name: "default code invalid", raw: "13800138000", def: "086", wantErr: ErrInvalid},
		{name: "unsupported separator", raw: "+86#1380013800", wantErr: ErrInvalid},
		{name: "max length accepted", raw: "+123456789012345", want: "+123456789012345"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeWithDefault(tt.raw, tt.def)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
				if got != "" {
					t.Fatalf("got %q, want empty on error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
			if !IsValid(got) {
				t.Fatalf("result %q is not valid E.164", got)
			}
		})
	}
}

func TestNormalizeMainland(t *testing.T) {
	valid := []string{"13800138000", "+8613800138000", "0086 138 0013 8000", "+86 (138) 0013-8000", "8613800138000"}
	for _, raw := range valid {
		got, err := NormalizeMainland(raw)
		if err != nil {
			t.Fatalf("NormalizeMainland(%q) error: %v", raw, err)
		}
		if got != "+8613800138000" {
			t.Fatalf("NormalizeMainland(%q) = %q", raw, got)
		}
	}

	// 8613800138000 没有国际前缀：数字段以 "86" 开头且长度合法，按裸号码补默认码会得到错误结果，
	// 因此这类输入必须由调用方带 "+"；默认策略是拒绝而不是猜。
	rejected := []string{"", "+861234567890", "+861380013800", "+14155552671", "+86108001380"}
	for _, raw := range rejected {
		if _, err := NormalizeMainland(raw); err == nil {
			t.Fatalf("NormalizeMainland(%q) unexpectedly accepted", raw)
		}
	}
}

func TestCountryCode(t *testing.T) {
	tests := []struct {
		value string
		code  string
		known bool
	}{
		{value: "+8613800138000", code: "86", known: true},
		{value: "+14155552671", code: "1", known: true},
		// NANP 共用首码 1 的目的地必须优先于 "1" 命中，否则会算进美国档。
		{value: "+12423334444", code: "1242", known: true},
		{value: "+18761234567", code: "1876", known: true},
		{value: "+447123456789", code: "44", known: true},
		{value: "+79123456789", code: "7", known: true},
		{value: "+8521234567", code: "852", known: true},
		{value: "+376123456", code: "376", known: true},
		// 未收录号段：known=false，调用方默认拒绝。
		{value: "+3891234567", code: "389", known: true},
		{value: "+91123456789", code: "91", known: true},
		{value: "+9990000000", code: "", known: false},
		{value: "8613800138000", code: "", known: false},
		{value: "", code: "", known: false},
	}
	for _, tt := range tests {
		code, known := CountryCode(tt.value)
		if code != tt.code || known != tt.known {
			t.Fatalf("CountryCode(%q) = (%q, %v), want (%q, %v)", tt.value, code, known, tt.code, tt.known)
		}
	}
}

func TestIsMainlandMobile(t *testing.T) {
	yes := []string{"+8613800138000", "+8619912345678", "+8614712345678"}
	no := []string{"+861234567890", "+861081234567", "+861380013800", "+14155552671", "+8521234567", ""}
	for _, value := range yes {
		if !IsMainlandMobile(value) {
			t.Fatalf("IsMainlandMobile(%q) = false, want true", value)
		}
	}
	for _, value := range no {
		if IsMainlandMobile(value) {
			t.Fatalf("IsMainlandMobile(%q) = true, want false", value)
		}
	}
}

func TestMask(t *testing.T) {
	tests := []struct {
		value string
		want  string
	}{
		{value: "+8613800138000", want: "+8613****00"},
		{value: "+14155552671", want: "+141****71"},
		{value: "+12423334444", want: "+124233****44"},
		{value: "8613800138000", want: maskStars},
		{value: "", want: maskStars},
		{value: "+9990000000", want: maskStars},
	}
	for _, tt := range tests {
		got := Mask(tt.value)
		if got != tt.want {
			t.Fatalf("Mask(%q) = %q, want %q", tt.value, got, tt.want)
		}
		// 脱敏输出只保留国家码与首尾各两位，不得留下完整订阅号。
		if strings.Contains(got, strings.TrimPrefix(tt.value, "+")) && tt.value != "" {
			t.Fatalf("Mask(%q) leaked the full number", tt.value)
		}
	}
}

func TestDigitsAndIsValid(t *testing.T) {
	if got := Digits("+8613800138000"); got != "8613800138000" {
		t.Fatalf("Digits = %q", got)
	}
	if got := Digits("8613800138000"); got != "" {
		t.Fatalf("Digits on non-canonical = %q, want empty", got)
	}
	if IsValid("+1234567") {
		t.Fatal("IsValid accepted a 7-digit number")
	}
	if !IsValid("+12345678") {
		t.Fatal("IsValid rejected an 8-digit number")
	}
}

func TestParseCountryCode(t *testing.T) {
	if got := ParseCountryCode("+86"); got != "86" {
		t.Fatalf("ParseCountryCode(+86) = %q", got)
	}
	if got := ParseCountryCode(" 86 "); got != "86" {
		t.Fatalf("ParseCountryCode(' 86 ') = %q", got)
	}
	if CountryName("86") != "CN" {
		t.Fatalf("CountryName(86) = %q", CountryName("86"))
	}
	codes := SupportedCountryCodes()
	if len(codes) < 100 {
		t.Fatalf("SupportedCountryCodes returned only %d entries", len(codes))
	}
	for i := 1; i < len(codes); i++ {
		if codes[i-1] > codes[i] {
			t.Fatalf("SupportedCountryCodes is not sorted at index %d", i)
		}
	}
}
