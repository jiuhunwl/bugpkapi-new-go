package secretbox

import (
	"errors"
	"strings"
	"testing"
)

func TestCipherEncryptsAndDecryptsVersionedSecrets(t *testing.T) {
	cipher := New("test-system-config-secret-that-is-at-least-32-bytes")
	encrypted, err := cipher.Encrypt("smtp-authorization-code")
	if err != nil {
		t.Fatalf("Encrypt() error = %v", err)
	}
	if !IsEncrypted(encrypted) || strings.Contains(encrypted, "smtp-authorization-code") {
		t.Fatalf("encrypted value is not protected: %q", encrypted)
	}
	plain, err := cipher.DecryptCompatible(encrypted)
	if err != nil || plain != "smtp-authorization-code" {
		t.Fatalf("DecryptCompatible() = (%q, %v)", plain, err)
	}
}

func TestCipherKeepsLegacyPlaintextReadable(t *testing.T) {
	plain, err := New("").DecryptCompatible("legacy-plain-secret")
	if err != nil || plain != "legacy-plain-secret" {
		t.Fatalf("legacy plaintext = (%q, %v)", plain, err)
	}
}

func TestCipherRejectsMissingOrWrongKeysForEncryptedData(t *testing.T) {
	encrypted, err := New("first-system-config-secret-that-is-at-least-32-bytes").Encrypt("secret")
	if err != nil {
		t.Fatalf("Encrypt() error = %v", err)
	}
	if _, err := New("").DecryptCompatible(encrypted); !errors.Is(err, ErrSecretMissing) {
		t.Fatalf("missing-key error = %v", err)
	}
	if _, err := New("second-system-config-secret-that-is-at-least-32-bytes").DecryptCompatible(encrypted); err == nil {
		t.Fatal("wrong key must not decrypt encrypted configuration")
	}
}
