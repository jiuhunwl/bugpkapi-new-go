package openapi

import (
	"errors"
	"strings"
	"testing"
	"time"

	apperrors "bugpk-api/internal/pkg/errors"

	"github.com/go-sql-driver/mysql"
)

// errFakeDuplicate 是非 1062 的普通错误，用于验证不误判。
var errFakeDuplicate = errors.New("connection refused")

// dupKeyErr 构造与 driver 真实返回形状一致的 1062 错误。
func dupKeyErr(index string) error {
	return &mysql.MySQLError{
		Number:  1062,
		Message: "Duplicate entry 'x' for key '" + index + "'",
	}
}

// errDetails 取出 BusinessError.Data 中的稳定错误码载荷。
func errDetails(err error) (ErrorCode, bool) {
	var biz *apperrors.BusinessError
	if !errors.As(err, &biz) {
		return ErrorCode{}, false
	}
	code, ok := biz.Data.(ErrorCode)
	return code, ok
}

func TestNewKeySecretMarkerAndShape(t *testing.T) {
	normal, err := newKeySecret(false)
	if err != nil {
		t.Fatalf("newKeySecret: %v", err)
	}
	admin, err := newKeySecret(true)
	if err != nil {
		t.Fatalf("newKeySecret: %v", err)
	}
	if !strings.HasPrefix(normal, KeyMarkerNormal) || strings.HasPrefix(normal, KeyMarkerAdmin) {
		t.Fatalf("normal key prefix = %q", normal[:5])
	}
	if !strings.HasPrefix(admin, KeyMarkerAdmin) {
		t.Fatalf("admin key prefix = %q", admin[:5])
	}
	if len(normal) != len(KeyMarkerNormal)+keySecretBytes*2 {
		t.Fatalf("normal key length = %d", len(normal))
	}
	if len(admin) != len(KeyMarkerAdmin)+keySecretBytes*2 {
		t.Fatalf("admin key length = %d", len(admin))
	}
	// 熵与去重：不同 Key 摘要必须不同。
	seen := map[string]struct{}{}
	for i := 0; i < 200; i++ {
		secret, err := newKeySecret(i%2 == 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, dup := seen[hashKey(secret)]; dup {
			t.Fatalf("duplicate secret generated: %q", secret)
		}
		seen[hashKey(secret)] = struct{}{}
	}
}

func TestHashKeyIsStableHex64(t *testing.T) {
	secret := KeyMarkerNormal + strings.Repeat("ab", 32)
	first := hashKey(secret)
	if len(first) != 64 {
		t.Fatalf("hash length = %d, want 64", len(first))
	}
	if first != hashKey("  "+secret+"  ") {
		t.Fatal("hashKey must trim surrounding whitespace")
	}
	if first == hashKey(secret+"x") {
		t.Fatal("different secrets must hash differently")
	}
}

func TestMaskKeyKeepsMarkerAndHidesBody(t *testing.T) {
	body := strings.Repeat("1234567890abcdef", 4) // 64 hex
	normal := KeyMarkerNormal + body
	admin := KeyMarkerAdmin + body
	if got := maskKey(normal); got != KeyMarkerNormal+"1234"+"****"+"cdef" {
		t.Fatalf("normal mask = %q", got)
	}
	if got := maskKey(admin); got != KeyMarkerAdmin+"1234"+"****"+"cdef" {
		t.Fatalf("admin mask = %q", got)
	}
	// 掩码绝不等于明文；中段不得出现。
	masked := maskKey(admin)
	if masked == admin || strings.Contains(masked, "567890ab") {
		t.Fatalf("mask leaked body: %q", masked)
	}
	if got := maskKey(KeyMarkerNormal + "abc"); got != KeyMarkerNormal+"****" {
		t.Fatalf("short body mask = %q", got)
	}
}

func TestNormalizeScopes(t *testing.T) {
	scopes, err := normalizeScopes([]string{" openapi:redeem:write ", EndpointAccountRead, EndpointAccountRead, ""})
	if err != nil {
		t.Fatalf("normalizeScopes: %v", err)
	}
	want := []string{EndpointAccountRead, EndpointRedeemWrite}
	if len(scopes) != len(want) || scopes[0] != want[0] || scopes[1] != want[1] {
		t.Fatalf("normalizeScopes = %v, want %v (目录顺序 + 去重)", scopes, want)
	}
	if _, err := normalizeScopes([]string{"openapi:evil:write"}); err == nil {
		t.Fatal("unknown endpoint code must be rejected")
	}
	if empty, err := normalizeScopes(nil); err != nil || len(empty) != 0 {
		t.Fatalf("empty scopes = %v, %v", empty, err)
	}
}

func TestHasAdminScope(t *testing.T) {
	if hasAdminScope([]string{EndpointAccountRead}) {
		t.Fatal("ordinary scopes must not grant admin scope")
	}
	if !hasAdminScope([]string{EndpointAccountRead, EndpointRedeemCodeGenerate}) {
		t.Fatal("generate endpoint must derive admin scope")
	}
}

func TestNormalizeIPRule(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"ipv4", "10.0.0.1", "10.0.0.1"},
		{"ipv4 cidr", "10.0.0.0/8", "10.0.0.0/8"},
		{"ipv4 mapped ipv6", "::ffff:10.0.0.7", "10.0.0.7"},
		{"ipv6", "2001:db8::1", "2001:db8::1"},
		{"trimmed", "  192.168.1.10  ", "192.168.1.10"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := normalizeIPRule(tc.input)
			if err != nil {
				t.Fatalf("normalizeIPRule(%q): %v", tc.input, err)
			}
			if got != tc.want {
				t.Fatalf("normalizeIPRule(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
	invalid := []string{"", "0.0.0.0/0", "::/0", "not-an-ip", "10.0.0.0/33", "300.1.1.1/24"}
	for _, input := range invalid {
		if _, err := normalizeIPRule(input); err == nil {
			t.Fatalf("normalizeIPRule(%q) should fail", input)
		}
	}
}

func TestIPAllowed(t *testing.T) {
	cases := []struct {
		name     string
		rules    []string
		clientIP string
		want     bool
	}{
		{"empty rules allow all", nil, "1.2.3.4", true},
		{"exact hit", []string{"10.0.0.1"}, "10.0.0.1", true},
		{"exact miss", []string{"10.0.0.1"}, "10.0.0.2", false},
		{"cidr hit", []string{"10.0.0.0/24"}, "10.0.0.55", true},
		{"cidr miss", []string{"10.0.0.0/24"}, "10.0.1.1", false},
		{"mapped client vs v4 rule", []string{"10.0.0.1"}, "::ffff:10.0.0.1", true},
		{"v4 client vs mapped rule", []string{"10.0.0.1"}, "10.0.0.1", true},
		{"garbage client", []string{"10.0.0.1"}, "не-ip", false},
		{"empty client with rules", []string{"10.0.0.1"}, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ipAllowed(tc.rules, tc.clientIP); got != tc.want {
				t.Fatalf("ipAllowed(%v, %q) = %t, want %t", tc.rules, tc.clientIP, got, tc.want)
			}
		})
	}
}

func TestIdempotencyScopeBindsKeyAndBody(t *testing.T) {
	body := []byte(`{"name":"a"}`)
	base := idempotencyScope(7, "POST", "/open-api/api-keys", body, "request-A")
	if base == idempotencyScope(8, "POST", "/open-api/api-keys", body, "request-A") {
		t.Fatal("scope must include key id")
	}
	if base == idempotencyScope(7, "PUT", "/open-api/api-keys", body, "request-A") {
		t.Fatal("scope must include method")
	}
	if base == idempotencyScope(7, "POST", "/open-api/orders", body, "request-A") {
		t.Fatal("scope must include path")
	}
	if base == idempotencyScope(7, "POST", "/open-api/api-keys", []byte(`{"name":"b"}`), "request-A") {
		t.Fatal("scope must include normalized body (S-5 同键不同体不得误判)")
	}
	if base != idempotencyScope(7, "post", "/open-api/api-keys", body, "request-A") {
		t.Fatal("method case must not change the scope")
	}
	if !strings.HasPrefix(base, "openapi:idem:") {
		t.Fatalf("scope prefix = %q", base)
	}
	if base == idempotencyScope(7, "POST", "/open-api/api-keys", body, "request-B") {
		t.Fatal("scope must include the client request key")
	}
	if got := idempotencyScope(7, "POST", "/open-api/api-keys", body, " \t"); got != "" {
		t.Fatalf("absent client key must disable caching, got %q", got)
	}
}

func TestKeyStatusText(t *testing.T) {
	now := time.Now()
	active := keyRow{Status: KeyStatusActive, ExpiresAt: now.Add(time.Hour)}
	if got := keyStatusText(active, now); got != "active" {
		t.Fatalf("active = %q", got)
	}
	expired := keyRow{Status: KeyStatusActive, ExpiresAt: now.Add(-time.Minute)}
	if got := keyStatusText(expired, now); got != "expired" {
		t.Fatalf("expired = %q", got)
	}
	revoked := keyRow{Status: KeyStatusRevoked, ExpiresAt: now.Add(time.Hour)}
	if got := keyStatusText(revoked, now); got != "revoked" {
		t.Fatalf("revoked = %q", got)
	}
}

func TestErrorCodeExposesStableHeader(t *testing.T) {
	headers := ErrorCode{Code: CodeScopeDenied}.ResponseHeaders()
	if headers["X-Bugpk-Openapi-Code"] != CodeScopeDenied {
		t.Fatalf("headers = %v", headers)
	}
	err := openError(CodeKeyLimit, 409, "已有有效 Key")
	details, ok := errDetails(err)
	if !ok || details.Code != CodeKeyLimit {
		t.Fatalf("openError data = %v (ok=%t)", details, ok)
	}
}

func TestIsDuplicateIndex(t *testing.T) {
	if isDuplicateIndex(errFakeDuplicate, "uk_open_api_key_active_user") {
		t.Fatal("unrelated error must not match")
	}
	if !isDuplicateIndex(dupKeyErr("uk_open_api_key_active_user"), "uk_open_api_key_active_user") {
		t.Fatal("matching index must be detected")
	}
	if isDuplicateIndex(dupKeyErr("uk_open_api_key_hash"), "uk_open_api_key_active_user") {
		t.Fatal("index name must be matched precisely")
	}
}

func TestToOpenAPIKeyKeepsMarkerInMask(t *testing.T) {
	row := keyRow{
		ID: 3, UserID: 9, Username: "u", Name: "n",
		KeyPrefix: "abcd", KeySuffix: "wxyz", IsAdminScope: true,
		Status:    KeyStatusActive,
		ExpiresAt: time.Now().Add(time.Hour),
		CreatedAt: time.Now(),
	}
	item := toOpenAPIKey(row, []string{EndpointAccountRead}, []string{"10.0.0.1"})
	if item.MaskedKey != KeyMarkerAdmin+"abcd****wxyz" {
		t.Fatalf("masked = %q", item.MaskedKey)
	}
	if item.ID != "3" || item.UserID != "9" || !item.IsAdminScope {
		t.Fatalf("item = %+v", item)
	}
}
