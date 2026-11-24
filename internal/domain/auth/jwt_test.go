package auth

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"bugpk-api/internal/config"

	"github.com/golang-jwt/jwt/v5"
)

func TestJWTRejectsMissingRequiredClaims(t *testing.T) {
	cfg := config.Default().Auth
	service, err := NewJWTService(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for name, claims := range map[string]jwt.MapClaims{
		"expiration": {"iss": cfg.Issuer, "iat": time.Now().Unix(), "uid": 1, "sub": "user", "tokenType": tokenTypeAccess},
		"issued-at":  {"iss": cfg.Issuer, "exp": time.Now().Add(time.Hour).Unix(), "uid": 1, "sub": "user", "tokenType": tokenTypeAccess},
		"subject":    {"iss": cfg.Issuer, "iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix(), "uid": 0, "sub": "", "tokenType": tokenTypeAccess},
		"type":       {"iss": cfg.Issuer, "iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix(), "uid": 1, "sub": "user", "tokenType": "other"},
	} {
		t.Run(name, func(t *testing.T) {
			token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(service.key)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := service.ParseAccessToken(token); err == nil {
				t.Fatalf("token missing/invalid %s was accepted", name)
			}
		})
	}
}

func TestJWTRefreshDeviceFingerprint(t *testing.T) {
	cfg := config.Default().Auth
	jwtService, err := NewJWTService(cfg)
	if err != nil {
		t.Fatalf("NewJWTService() error = %v", err)
	}
	user := User{ID: 7, Username: "alice", Roles: []string{"user"}, TokenVersion: 3}
	device := DeviceFingerprint("127.0.0.1", "ua")
	token, jti, expiresAt, err := jwtService.CreateRefreshToken(user, device, time.Now())
	if err != nil {
		t.Fatalf("CreateRefreshToken() error = %v", err)
	}
	if jti == "" || expiresAt.IsZero() {
		t.Fatal("expected jti and expiration")
	}
	info, err := jwtService.ParseRefreshToken(token, device)
	if err != nil {
		t.Fatalf("ParseRefreshToken() error = %v", err)
	}
	if info.UserID != user.ID || info.TokenVersion != user.TokenVersion || info.JTI != jti {
		t.Fatalf("unexpected refresh info: %+v", info)
	}
	if _, err := jwtService.ParseRefreshToken(token, "bad-device"); err == nil {
		t.Fatal("expected bad device to fail")
	}
}

func TestDeviceFingerprintSurvivesIPChanges(t *testing.T) {
	first := DeviceFingerprint("198.51.100.10", "BugPk-Mobile/1.0")
	second := DeviceFingerprint("203.0.113.20", "BugPk-Mobile/1.0")
	if first != second {
		t.Fatalf("device fingerprint changed with IP: %q != %q", first, second)
	}
	if first != DeviceFingerprint("198.51.100.10", "BugPk-Mobile/2.0") {
		t.Fatal("device fingerprint must tolerate a client version upgrade")
	}
	if first == DeviceFingerprint("198.51.100.10", "BugPk-Desktop/1.0") {
		t.Fatal("device fingerprint must still distinguish different clients")
	}
	if len(first) <= len(deviceFingerprintV3Prefix) || first[:len(deviceFingerprintV3Prefix)] != deviceFingerprintV3Prefix {
		t.Fatalf("device fingerprint is missing v3 prefix: %q", first)
	}
}

func TestDeviceFingerprintIgnoresBrowserUpgrade(t *testing.T) {
	const template = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/%s Safari/537.36"
	chrome149 := DeviceFingerprint("198.51.100.10", fmt.Sprintf(template, "149.0.0.0"))
	chrome156 := DeviceFingerprint("198.51.100.10", fmt.Sprintf(template, "156.0.0.0"))
	if chrome149 != chrome156 {
		t.Fatalf("browser upgrade changed device fingerprint: %q != %q", chrome149, chrome156)
	}
	edge := DeviceFingerprint("198.51.100.10", strings.Replace(
		fmt.Sprintf(template, "156.0.0.0"), "Chrome/156.0.0.0", "Chrome/156.0.0.0 Edg/156.0.0.0", 1,
	))
	if edge == chrome156 {
		t.Fatal("different browser families produced the same fingerprint")
	}
	const iosTemplate = "Mozilla/5.0 (iPhone; CPU iPhone OS %s like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Mobile/15E148 Safari/604.1"
	if DeviceFingerprint("", fmt.Sprintf(iosTemplate, "18_6")) != DeviceFingerprint("", fmt.Sprintf(iosTemplate, "18_7")) {
		t.Fatal("iOS patch upgrade changed device fingerprint")
	}
}

func TestParseRefreshTokenForDeviceAcceptsV2Token(t *testing.T) {
	cfg := config.Default().Auth
	jwtService, err := NewJWTService(cfg)
	if err != nil {
		t.Fatal(err)
	}
	service := &Service{jwt: jwtService}
	user := User{ID: 11, Username: "v2", Roles: []string{"user"}}
	const ip = "198.51.100.10"
	const userAgent = "BugPk-Web/1.0"
	token, _, _, err := jwtService.CreateRefreshToken(user, deviceFingerprintV2(ip, userAgent), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.parseRefreshTokenForDevice(token, ip, userAgent); err != nil {
		t.Fatalf("v2 refresh token was not accepted: %v", err)
	}
}

func TestParseRefreshTokenForDeviceSupportsLegacyRotation(t *testing.T) {
	cfg := config.Default().Auth
	jwtService, err := NewJWTService(cfg)
	if err != nil {
		t.Fatal(err)
	}
	service := &Service{jwt: jwtService}
	user := User{ID: 9, Username: "legacy", Roles: []string{"user"}}
	const ip = "198.51.100.10"
	const userAgent = "BugPk-Web/1.0"
	token, _, _, err := jwtService.CreateRefreshToken(user, legacyDeviceFingerprint(ip, userAgent), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.parseRefreshTokenForDevice(token, ip, userAgent); err != nil {
		t.Fatalf("legacy refresh token was not accepted on its original device: %v", err)
	}
	if _, err := service.parseRefreshTokenForDevice(token, "203.0.113.20", userAgent); err == nil {
		t.Fatal("legacy refresh token unexpectedly ignored its original IP binding")
	}
}

func TestParseRefreshTokenForDeviceAcceptsNewTokenAfterIPChange(t *testing.T) {
	cfg := config.Default().Auth
	jwtService, err := NewJWTService(cfg)
	if err != nil {
		t.Fatal(err)
	}
	service := &Service{jwt: jwtService}
	user := User{ID: 10, Username: "mobile", Roles: []string{"user"}}
	const userAgent = "BugPk-Mobile/1.0"
	token, _, _, err := jwtService.CreateRefreshToken(user, DeviceFingerprint("198.51.100.10", userAgent), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.parseRefreshTokenForDevice(token, "203.0.113.20", userAgent); err != nil {
		t.Fatalf("new refresh token failed after IP change: %v", err)
	}
}

// 后台登录态配置的运行时覆盖：ApplySessionTTL 后新签发令牌的 exp 按 override
// 计算，cookie MaxAge 与签发一致；恢复默认（0 值）后回落到 config 静态值。
// exp 落到 Unix 秒后天然有亚秒舍入，断言统一允许 2 秒容差。
func ttlWithin(got, want time.Duration) bool {
	diff := got - want
	if diff < 0 {
		diff = -diff
	}
	return diff <= 2*time.Second
}

func TestJWTSessionTTLOverride(t *testing.T) {
	cfg := config.Default().Auth
	service, err := NewJWTService(cfg)
	if err != nil {
		t.Fatal(err)
	}
	user := User{ID: 7, Username: "ttl-user"}
	now := time.Now()

	// 未覆盖：默认 30 分钟。
	token, err := service.CreateAccessToken(user, now)
	if err != nil {
		t.Fatal(err)
	}
	info, err := service.ParseAccessToken(token)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.ExpiresAt.Sub(now); !ttlWithin(got, cfg.AccessTokenTTL) {
		t.Fatalf("default access ttl = %v, want %v", got, cfg.AccessTokenTTL)
	}

	// 覆盖为 120 分钟 / 30 天。
	service.ApplySessionTTL(120, 30)
	if got := service.AccessTTL(); got != 120*time.Minute {
		t.Fatalf("override access ttl = %v, want 120m", got)
	}
	if got := service.RefreshTTL(); got != 30*24*time.Hour {
		t.Fatalf("override refresh ttl = %v, want 30d", got)
	}
	token, err = service.CreateAccessToken(user, now)
	if err != nil {
		t.Fatal(err)
	}
	info, err = service.ParseAccessToken(token)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.ExpiresAt.Sub(now); !ttlWithin(got, 120*time.Minute) {
		t.Fatalf("overridden access token ttl = %v, want 120m", got)
	}
	_, jti, expiresAt, err := service.CreateRefreshToken(user, "ua2_x", now)
	if err != nil || jti == "" {
		t.Fatalf("CreateRefreshToken() = (%q, %v), err=%v", jti, expiresAt, err)
	}
	if got := expiresAt.Sub(now); !ttlWithin(got, 30*24*time.Hour) {
		t.Fatalf("overridden refresh token ttl = %v, want 30d", got)
	}

	// 0 值恢复默认。
	service.ApplySessionTTL(0, 0)
	if got := service.AccessTTL(); got != cfg.AccessTokenTTL {
		t.Fatalf("restored access ttl = %v, want %v", got, cfg.AccessTokenTTL)
	}
	if got := service.RefreshTTL(); got != cfg.RefreshTokenTTL {
		t.Fatalf("restored refresh ttl = %v, want %v", got, cfg.RefreshTokenTTL)
	}
}
