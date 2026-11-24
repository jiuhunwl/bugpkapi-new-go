package linkparam

import "testing"

func TestClean(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"普通文本 abc 123", "普通文本 abc 123"},
		{"  hello world  ", "  hello world  "},
		{"复制 https://example.com/v?id=12&sign=a%2Fb+z 查看", "https://example.com/v?id=12&sign=a%2Fb+z"},
		{"链接（https://example.com/a）。", "https://example.com/a"},
		{"https://one.test/a https://two.test/b", "https://one.test/a"},
		{"https://example.com/a_(b)", "https://example.com/a_(b)"},
		{"不是 https:// 或 ftp://example.com", "不是 https:// 或 ftp://example.com"},
		{"https://example.com/a?x=! 分享", "https://example.com/a?x=!"},
	} {
		if got := Clean(tc.in); got != tc.want {
			t.Errorf("Clean(%q)=%q want %q", tc.in, got, tc.want)
		}
	}
}
func TestProtectedFields(t *testing.T) {
	for _, name := range []string{"key", "api_key", "X-API-Key", "accessToken", "signature", "Authorization", "password"} {
		if !ProtectedName(name) {
			t.Error(name)
		}
	}
	if ProtectedName("url") {
		t.Fatal("business field protected")
	}
}
