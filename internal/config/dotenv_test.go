package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// clearEnv 清除指定环境变量并在测试结束时恢复原值。
func clearEnv(t *testing.T, keys ...string) {
	t.Helper()
	for _, key := range keys {
		old, existed := os.LookupEnv(key)
		t.Cleanup(func() {
			if existed {
				_ = os.Setenv(key, old)
			} else {
				_ = os.Unsetenv(key)
			}
		})
		_ = os.Unsetenv(key)
	}
}

func writeEnvFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write env file: %v", err)
	}
	return path
}

func TestLoadDotEnvFileBasic(t *testing.T) {
	clearEnv(t, "DOTENV_TEST_A", "DOTENV_TEST_B", "DOTENV_TEST_C", "DOTENV_TEST_D", "DOTENV_TEST_E")
	path := writeEnvFile(t, `
# 注释行
DOTENV_TEST_A=hello
DOTENV_TEST_B=hello world

export DOTENV_TEST_C=exported
DOTENV_TEST_D='single quoted'
DOTENV_TEST_E="double quoted with \n newline and \" quote"
`)

	loaded, err := loadDotEnvFile(path)
	if err != nil {
		t.Fatalf("loadDotEnvFile() error = %v", err)
	}
	if !loaded {
		t.Fatal("expected env file to be loaded")
	}

	if got := os.Getenv("DOTENV_TEST_A"); got != "hello" {
		t.Errorf("DOTENV_TEST_A = %q, want %q", got, "hello")
	}
	if got := os.Getenv("DOTENV_TEST_B"); got != "hello world" {
		t.Errorf("DOTENV_TEST_B = %q, want %q", got, "hello world")
	}
	if got := os.Getenv("DOTENV_TEST_C"); got != "exported" {
		t.Errorf("DOTENV_TEST_C = %q, want %q", got, "exported")
	}
	if got := os.Getenv("DOTENV_TEST_D"); got != "single quoted" {
		t.Errorf("DOTENV_TEST_D = %q, want %q", got, "single quoted")
	}
	if got := os.Getenv("DOTENV_TEST_E"); got != "double quoted with \n newline and \" quote" {
		t.Errorf("DOTENV_TEST_E = %q", got)
	}
}

func TestLoadDotEnvFileCRLFAndInlineComment(t *testing.T) {
	clearEnv(t, "DOTENV_TEST_F", "DOTENV_TEST_G")
	path := writeEnvFile(t, "DOTENV_TEST_F=value-with-crlf\r\nDOTENV_TEST_G=plain # 行内注释\r\n")

	if _, err := loadDotEnvFile(path); err != nil {
		t.Fatalf("loadDotEnvFile() error = %v", err)
	}
	if got := os.Getenv("DOTENV_TEST_F"); got != "value-with-crlf" {
		t.Errorf("DOTENV_TEST_F = %q, want %q (CRLF must be stripped)", got, "value-with-crlf")
	}
	if got := os.Getenv("DOTENV_TEST_G"); got != "plain" {
		t.Errorf("DOTENV_TEST_G = %q, want %q (inline comment must be stripped)", got, "plain")
	}
}

func TestLoadDotEnvFileDoesNotOverrideExistingEnv(t *testing.T) {
	clearEnv(t, "DOTENV_TEST_H")
	_ = os.Setenv("DOTENV_TEST_H", "from-process-env")
	path := writeEnvFile(t, "DOTENV_TEST_H=from-env-file\n")

	if _, err := loadDotEnvFile(path); err != nil {
		t.Fatalf("loadDotEnvFile() error = %v", err)
	}
	if got := os.Getenv("DOTENV_TEST_H"); got != "from-process-env" {
		t.Errorf("DOTENV_TEST_H = %q, want %q (existing env must win)", got, "from-process-env")
	}
}

func TestLoadDotEnvFileHashInUnquotedValue(t *testing.T) {
	clearEnv(t, "DOTENV_TEST_I")
	path := writeEnvFile(t, "DOTENV_TEST_I=abc#def\n") // # 前无空白，不应被当作注释

	if _, err := loadDotEnvFile(path); err != nil {
		t.Fatalf("loadDotEnvFile() error = %v", err)
	}
	if got := os.Getenv("DOTENV_TEST_I"); got != "abc#def" {
		t.Errorf("DOTENV_TEST_I = %q, want %q", got, "abc#def")
	}
}

func TestLoadDotEnvFileMalformed(t *testing.T) {
	path := writeEnvFile(t, "THIS_LINE_HAS_NO_EQUALS\n")
	if _, err := loadDotEnvFile(path); err == nil {
		t.Fatal("expected error for line without '='")
	} else if !strings.Contains(err.Error(), "missing '='") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestLoadDotEnvFileMissing(t *testing.T) {
	loaded, err := loadDotEnvFile(filepath.Join(t.TempDir(), "not-exists.env"))
	if err != nil {
		t.Fatalf("loadDotEnvFile() error = %v", err)
	}
	if loaded {
		t.Fatal("expected loaded=false for missing file")
	}
}

func TestParseEnvValue(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{in: `"plain"`, want: "plain"},
		{in: `"a\nb"`, want: "a\nb"},
		{in: `"tab\there"`, want: "tab\there"},
		{in: `"quote\"inside"`, want: `quote"inside`},
		{in: `"back\\slash"`, want: `back\slash`},
		{in: `'raw\nnot-escaped'`, want: `raw\nnot-escaped`},
		{in: `  padded  `, want: "padded"},
		{in: `value with " #comment`, want: `value with "`},
		{in: `value#not-comment`, want: "value#not-comment"},
	}
	for _, tc := range cases {
		if got := parseEnvValue(tc.in); got != tc.want {
			t.Errorf("parseEnvValue(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
