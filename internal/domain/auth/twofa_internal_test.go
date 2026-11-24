package auth

import (
	"testing"
	"time"

	"bugpk-api/internal/config"
)

func TestIsAdminRole(t *testing.T) {
	cases := []struct {
		roles []string
		want  bool
	}{
		{[]string{"admin"}, true},
		{[]string{"user", "ADMIN"}, true},
		{[]string{" user ", "admin "}, true},
		{[]string{"user"}, false},
		{nil, false},
		{[]string{"administrator"}, false},
	}
	for _, tc := range cases {
		if got := IsAdminRole(tc.roles); got != tc.want {
			t.Fatalf("IsAdminRole(%v) = %v, want %v", tc.roles, got, tc.want)
		}
	}
}

func TestTwoFAChallengeTokenRoundtrip(t *testing.T) {
	cfg := config.AuthConfig{
		Secret:            "unit-test-secret-0123456789abcdef0123456789abcdef",
		Issuer:            "totp-unit",
		AccessTokenTTL:    time.Hour,
		RefreshTokenTTL:   24 * time.Hour,
		TwoFAChallengeTTL: 5 * time.Minute,
	}
	svc, err := NewJWTService(cfg)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	user := User{ID: 42, Username: "admin", TokenVersion: 3, Roles: []string{"admin"}}
	token, expiresAt, err := svc.CreateTwoFAChallengeToken(user, now)
	if err != nil {
		t.Fatal(err)
	}
	// 返回值保留原始精度，仅校验其相对 now 的 TTL 正确（JWT 内的 exp
	// 是秒级截断的，这里比的是函数返回值）。
	if want := now.Add(cfg.TwoFAChallengeTTL); expiresAt.Sub(want) > time.Millisecond || want.Sub(expiresAt) > time.Millisecond {
		t.Fatalf("expiresAt = %v, want %v", expiresAt, want)
	}
	info, err := svc.ParseTwoFAChallengeToken(token)
	if err != nil {
		t.Fatal(err)
	}
	if info.UserID != 42 || info.Username != "admin" || info.TokenVersion != 3 {
		t.Fatalf("unexpected info: %+v", info)
	}
}

func TestTwoFAChallengeTokenRejectsOtherTokenTypes(t *testing.T) {
	cfg := config.AuthConfig{
		Secret:          "unit-test-secret-0123456789abcdef0123456789abcdef",
		Issuer:          "totp-unit",
		AccessTokenTTL:  time.Hour,
		RefreshTokenTTL: 24 * time.Hour,
	}
	svc, err := NewJWTService(cfg)
	if err != nil {
		t.Fatal(err)
	}
	user := User{ID: 7, Username: "u", TokenVersion: 1}
	access, err := svc.CreateAccessToken(user, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	// access token 不能冒充 2FA challenge。
	if _, err := svc.ParseTwoFAChallengeToken(access); err == nil {
		t.Fatal("access token must not be accepted as 2FA challenge")
	}
}

func TestGenerateRecoveryCodesFormat(t *testing.T) {
	codes, hashes, err := generateRecoveryCodes(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(codes) != 10 || len(hashes) != 10 {
		t.Fatalf("expected 10 codes/hashes, got %d/%d", len(codes), len(hashes))
	}
	seen := map[string]bool{}
	for i, code := range codes {
		if len(code) != 11 || code[5] != '-' {
			t.Fatalf("unexpected code format: %q", code)
		}
		if seen[code] {
			t.Fatalf("duplicate recovery code: %s", code)
		}
		seen[code] = true
		if len(hashes[i]) != 64 {
			t.Fatalf("hash must be sha256 hex, got %q", hashes[i])
		}
	}
}

func TestTwoFAPendingKeyFormat(t *testing.T) {
	if got := twoFAPendingKey(123); got != "auth:2fa:pending:123" {
		t.Fatalf("unexpected key: %s", got)
	}
}
