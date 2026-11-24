package auth

import (
	"strings"
	"testing"
)

func TestSecretRedisKeysDoNotContainBearerValues(t *testing.T) {
	for name, test := range map[string]struct {
		key    string
		prefix string
		secret string
	}{
		"captcha":        {captchaKey("captcha-secret-id"), "auth:captcha:", "captcha-secret-id"},
		"password reset": {passwordResetTokenKey("reset-secret-token"), "auth:password_reset:token:", "reset-secret-token"},
	} {
		t.Run(name, func(t *testing.T) {
			if strings.Contains(test.key, test.secret) {
				t.Fatalf("Redis key leaked bearer value: %q", test.key)
			}
			if !strings.HasPrefix(test.key, test.prefix) || len(strings.TrimPrefix(test.key, test.prefix)) != 64 {
				t.Fatalf("Redis key has unexpected format: %q", test.key)
			}
		})
	}
}

func TestSecretRedisKeyIsStableAndDomainSeparated(t *testing.T) {
	first := secretRedisKey("one:", "same-secret")
	if first != secretRedisKey("one:", " same-secret ") {
		t.Fatal("secret key normalization is not stable")
	}
	if first == secretRedisKey("two:", "same-secret") {
		t.Fatal("Redis key prefixes must remain domain separated")
	}
}

func TestPasswordResetUserKeyContainsOnlyValidatedUserID(t *testing.T) {
	key := passwordResetUserKey(42)
	if key != "auth:password_reset:user:42" {
		t.Fatalf("password reset user key = %q", key)
	}
}
