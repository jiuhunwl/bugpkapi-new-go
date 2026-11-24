package totp

import (
	"strings"
	"testing"
	"time"
)

// rfcSecret 是 RFC 6238 附录 B 的 ASCII 测试密钥 "12345678901234567890"。
const rfcSecret = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"

func TestCodeRFC6238Vectors(t *testing.T) {
	// RFC 6238 附录 B（SHA1）8 位向量取低 6 位后的标准 6 位等价值。
	cases := []struct {
		unix int64
		want string
	}{
		{59, "287082"},
		{1111111109, "081804"},
		{1111111111, "050471"},
		{1234567890, "005924"},
		{2000000000, "279037"},
		{20000000000, "353130"},
	}
	for _, tc := range cases {
		got, err := Code(rfcSecret, time.Unix(tc.unix, 0).UTC())
		if err != nil {
			t.Fatalf("unix=%d: %v", tc.unix, err)
		}
		if got != tc.want {
			t.Fatalf("unix=%d: got %s want %s", tc.unix, got, tc.want)
		}
	}
}

func TestVerifyAcceptsNeighborWindow(t *testing.T) {
	t0 := time.Unix(1111111109, 0).UTC()
	// 当前窗口码在 t 和 t±30s 内都应通过（±1 窗口漂移）。
	for _, offset := range []time.Duration{0, Step, -Step} {
		ok, err := Verify(rfcSecret, "081804", t0.Add(offset))
		if err != nil {
			t.Fatalf("offset=%v: %v", offset, err)
		}
		if !ok {
			t.Fatalf("offset=%v: expected match", offset)
		}
	}
	// 超出 ±1 窗口必须失败。
	ok, err := Verify(rfcSecret, "081804", t0.Add(2*Step))
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("expected reject beyond skew window")
	}
}

func TestVerifyRejectsWrongCodeAndFormat(t *testing.T) {
	t0 := time.Unix(1111111109, 0).UTC()
	for _, code := range []string{"000001", "0818041", "", "08-1804", "abcdef"} {
		ok, err := Verify(rfcSecret, code, t0)
		if err != nil {
			t.Fatalf("code=%q: unexpected error %v", code, err)
		}
		if ok {
			t.Fatalf("code=%q: expected reject", code)
		}
	}
}

func TestVerifyRejectsInvalidSecret(t *testing.T) {
	if _, err := Verify("not base32!!", "123456", time.Now()); err == nil {
		t.Fatal("expected error for undecodable secret")
	}
}

func TestGenerateSecretRoundtrip(t *testing.T) {
	secret, err := GenerateSecret()
	if err != nil {
		t.Fatal(err)
	}
	if len(secret) != 32 { // 20 bytes -> 32 base32 chars without padding
		t.Fatalf("unexpected secret length %d", len(secret))
	}
	if !ValidateSecret(secret) {
		t.Fatal("generated secret failed validation")
	}
	code, err := Code(secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	ok, err := Verify(secret, code, time.Now())
	if err != nil || !ok {
		t.Fatalf("roundtrip failed: ok=%v err=%v", ok, err)
	}
}

func TestValidateSecret(t *testing.T) {
	if ValidateSecret("") || ValidateSecret("AAAA") { // 太短
		t.Fatal("expected invalid secrets to be rejected")
	}
	if !ValidateSecret(strings.ToLower(rfcSecret)) {
		t.Fatal("lowercase secret should decode")
	}
}

func TestProvisioningURL(t *testing.T) {
	got := ProvisioningURL(rfcSecret, "BugPk-Api", "admin")
	if !strings.HasPrefix(got, "otpauth://totp/BugPk-Api:admin?") {
		t.Fatalf("unexpected label: %s", got)
	}
	if !strings.Contains(got, "secret="+rfcSecret) || !strings.Contains(got, "issuer=BugPk-Api") {
		t.Fatalf("missing params: %s", got)
	}
}
