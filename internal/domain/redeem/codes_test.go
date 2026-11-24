package redeem

import (
	"errors"
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"
)

// errFakeDuplicate 是非 MySQL 1062 的普通错误，用于验证 IsDuplicateKey 不误判。
var errFakeDuplicate = errors.New("connection refused")

// dupKeyErr 构造与 driver 真实返回形状一致的 1062 错误。
func dupKeyErr(index string) error {
	return &mysql.MySQLError{
		Number:  1062,
		Message: "Duplicate entry 'x' for key '" + index + "'",
	}
}

func TestGenerateCodeFormat(t *testing.T) {
	seen := make(map[string]struct{}, 2000)
	for i := 0; i < 2000; i++ {
		code, err := generateCode()
		if err != nil {
			t.Fatalf("generateCode: %v", err)
		}
		if len(code) != 19 {
			t.Fatalf("code length = %d, want 19 (%q)", len(code), code)
		}
		groups := strings.Split(code, "-")
		if len(groups) != codeGroups {
			t.Fatalf("groups = %d, want %d (%q)", len(groups), codeGroups, code)
		}
		for _, group := range groups {
			if len(group) != codeGroupLength {
				t.Fatalf("group length = %d, want %d (%q)", len(group), codeGroupLength, group)
			}
			for _, r := range group {
				if !strings.ContainsRune(codeAlphabet, r) {
					t.Fatalf("code %q contains形近字符 %q (must be excluded)", code, r)
				}
			}
		}
		if _, dup := seen[code]; dup {
			t.Fatalf("duplicate code generated: %q", code)
		}
		seen[code] = struct{}{}
	}
}

func TestGenerateCodeExcludesAmbiguousChars(t *testing.T) {
	// 0/O/1/I/L 必须不在字符集内（人工转录防错）。
	for _, banned := range "01OIL" {
		if strings.ContainsRune(codeAlphabet, banned) {
			t.Fatalf("alphabet contains ambiguous char %q", banned)
		}
	}
	if len(codeAlphabet) != 31 {
		t.Fatalf("alphabet size = %d, want 31", len(codeAlphabet))
	}
}

func TestNormalizeCode(t *testing.T) {
	valid := "ABCD-2345-EFGH-6789"
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"canonical", valid, "ABCD-2345-EFGH-6789"},
		{"lowercase", strings.ToLower(valid), "ABCD-2345-EFGH-6789"},
		{"spaces", " abcd 2345 efgh 6789 ", "ABCD-2345-EFGH-6789"},
		{"tabs", "abcd\t2345\tefgh\t6789", "ABCD-2345-EFGH-6789"},
		{"no separators", "ABCD2345EFGH6789", "ABCD-2345-EFGH-6789"},
		{"mixed separators", "abcd-2345 efgh-6789", "ABCD-2345-EFGH-6789"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := normalizeCode(tc.input)
			if err != nil {
				t.Fatalf("normalizeCode(%q): %v", tc.input, err)
			}
			if got != tc.want {
				t.Fatalf("normalizeCode(%q) = %q, want %q", tc.input, got, tc.want)
			}
			if len(got) != 19 {
				t.Fatalf("normalized length = %d, want 19 (canonical storage form)", len(got))
			}
		})
	}
	invalid := []string{"", "short", "ABCD-2345-EFGH-678", "ABCD2345EFGH6780", "abcd2345efgh678!", strings.Repeat("A", 17), "ABCD-2345-EFGH-678A-9"}
	for _, input := range invalid {
		if _, err := normalizeCode(input); err == nil {
			t.Fatalf("normalizeCode(%q) should fail", input)
		}
	}
}

func TestMaskCode(t *testing.T) {
	if got := maskCode("ABCD-2345-EFGH-6789"); got != "ABCD-****-****-6789" {
		t.Fatalf("maskCode = %q", got)
	}
	if got := maskCode("short"); got != "" {
		t.Fatalf("maskCode(short) = %q, want empty", got)
	}
}

func TestSafeCellEscapesFormulaPrefix(t *testing.T) {
	cases := map[string]string{
		"=1+1":       "'=1+1",
		"+cmd":       "'+cmd",
		"-2":         "'-2",
		"@SUM(A1)":   "'@SUM(A1)",
		" ABCD-1234": " ABCD-1234",
		"plain":      "plain",
		"":           "",
	}
	for input, want := range cases {
		if got := safeCell(input); got != want {
			t.Fatalf("safeCell(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestParseMoneyField(t *testing.T) {
	cases := []struct {
		name  string
		input any
		want  string
	}{
		{"nil", nil, "0.0000"},
		{"string", "10.5", "10.5000"},
		{"string padded", " 3 ", "3.0000"},
		{"float", 12.25, "12.2500"},
		{"int", 7, "7.0000"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseMoneyField(tc.input)
			if err != nil {
				t.Fatalf("parseMoneyField(%v): %v", tc.input, err)
			}
			if got != tc.want {
				t.Fatalf("parseMoneyField(%v) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
	// 注意：decimalvalue.ParseNonNegative 有意接受科学计数法（如 "1e5"），
	// 与仓内其他金额解析路径保持一致；这里只校验真正非法的输入。
	for _, bad := range []any{"-1", "abc", "1e", "0x10", "1,000", 99999999.9999 + 1} {
		if _, err := parseMoneyField(bad); err == nil {
			t.Fatalf("parseMoneyField(%v) should fail", bad)
		}
	}
}

func TestParseTimeField(t *testing.T) {
	got, err := parseTimeField("2026-10-14 12:30:00")
	if err != nil {
		t.Fatalf("parseTimeField: %v", err)
	}
	if got.Year() != 2026 || got.Month() != 10 || got.Day() != 14 {
		t.Fatalf("parsed = %v", got)
	}
	if _, err := parseTimeField("not-a-time"); err == nil {
		t.Fatal("parseTimeField should reject garbage")
	}
}

func TestIsDuplicateKey(t *testing.T) {
	if IsDuplicateKey(errFakeDuplicate, "uk_redeem_record_type_ip") {
		t.Fatal("unrelated index should not match")
	}
	if !IsDuplicateKey(dupKeyErr("uk_redeem_record_type_ip"), "uk_redeem_record_type_ip") {
		t.Fatal("matching index should be detected")
	}
}

func TestBatchSourceText(t *testing.T) {
	if got := batchSourceText(SourceConsole); got != "管理端生成" {
		t.Fatalf("console source = %q", got)
	}
	if got := batchSourceText(SourceOpenAPI); got != "开放API生成" {
		t.Fatalf("openapi source = %q", got)
	}
}

func TestExportFilenameSanitizesTypeCode(t *testing.T) {
	name := exportFilename(typeRow{Code: "../etc/passwd"})
	if strings.ContainsAny(name, "/\\") {
		t.Fatalf("filename not sanitized: %q", name)
	}
	if !strings.HasPrefix(name, "redeem-codes-") || !strings.HasSuffix(name, ".xlsx") {
		t.Fatalf("filename shape wrong: %q", name)
	}
}
