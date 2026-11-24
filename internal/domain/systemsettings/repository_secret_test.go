package systemsettings

import (
	"errors"
	"testing"

	"bugpk-api/internal/pkg/secretbox"
)

func TestMigrateStoredSecretEncryptsLegacyAndValidatesCiphertext(t *testing.T) {
	cipher := secretbox.New("system-settings-test-secret-that-is-at-least-32-bytes")
	migrated, changed, err := migrateStoredSecret(cipher, "legacy-secret")
	if err != nil || !changed || !secretbox.IsEncrypted(migrated) {
		t.Fatalf("migrate legacy = (%q, %v, %v)", migrated, changed, err)
	}
	validated, changed, err := migrateStoredSecret(cipher, migrated)
	if err != nil || changed || validated != migrated {
		t.Fatalf("validate encrypted = (%q, %v, %v)", validated, changed, err)
	}
	if _, _, err := migrateStoredSecret(secretbox.New(""), "legacy-secret"); !errors.Is(err, secretbox.ErrSecretMissing) {
		t.Fatalf("missing-key migration error = %v", err)
	}
	if _, _, err := migrateStoredSecret(secretbox.New("wrong-system-settings-secret-that-is-at-least-32-bytes"), migrated); !errors.Is(err, secretbox.ErrDecrypt) {
		t.Fatalf("wrong-key validation error = %v", err)
	}
}
