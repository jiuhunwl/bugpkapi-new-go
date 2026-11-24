package payment

import (
	"errors"
	"testing"
)

func TestConfigCipherRequiresSecret(t *testing.T) {
	_, err := NewConfigCipher("").Encrypt("secret")
	if !errors.Is(err, ErrPaymentSecretMissing) {
		t.Fatalf("expected ErrPaymentSecretMissing, got %v", err)
	}
}

func TestConfigCipherRoundTrip(t *testing.T) {
	cipher := NewConfigCipher("local-secret")
	encrypted, err := cipher.Encrypt("private-key")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	decrypted, err := cipher.Decrypt(encrypted)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if decrypted != "private-key" {
		t.Fatalf("unexpected decrypted value %q", decrypted)
	}
}
