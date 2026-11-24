package app

import (
	"testing"

	"bugpk-api/internal/config"

	"golang.org/x/crypto/bcrypt"
)

func TestLegacyInitialAdminHashIsKnownWeakPassword(t *testing.T) {
	if err := bcrypt.CompareHashAndPassword([]byte(legacyInitialAdminHash), []byte("123456")); err != nil {
		t.Fatalf("legacy admin hash no longer matches the guarded weak password: %v", err)
	}
}

func TestInitialAdminPasswordRequiresProductionSecret(t *testing.T) {
	_, err := initialAdminPassword(config.Config{App: config.AppConfig{Env: "production"}})
	if err == nil {
		t.Fatal("production bootstrap accepted an empty INITIAL_ADMIN_PASSWORD")
	}
}

func TestInitialAdminPasswordRequiresStagingSecret(t *testing.T) {
	_, err := initialAdminPassword(config.Config{App: config.AppConfig{Env: "staging"}})
	if err == nil {
		t.Fatal("staging bootstrap accepted an empty INITIAL_ADMIN_PASSWORD")
	}
}

func TestInitialAdminPasswordRequiresExplicitSecretInEveryEnvironment(t *testing.T) {
	for _, environment := range []string{"", "dev", "development", "test", "testing", "stage", "staging", "prod", "production"} {
		t.Run(environment, func(t *testing.T) {
			if _, err := initialAdminPassword(config.Config{App: config.AppConfig{Env: environment}}); err == nil {
				t.Fatal("bootstrap accepted an empty INITIAL_ADMIN_PASSWORD")
			}
		})
	}
}

func TestInitialAdminPasswordRejectsPublishedDefaults(t *testing.T) {
	for _, password := range []string{"123456", " 123456 ", "请设置8-64位强密码", "replace-with-a-unique-strong-bootstrap-password"} {
		if _, err := initialAdminPassword(config.Config{Auth: config.AuthConfig{InitialAdminPassword: password}}); err == nil {
			t.Fatal("bootstrap accepted a publicly documented default password")
		}
	}
}

func TestPublishedInitialAdminHashesAreRecognizedAfterRehashing(t *testing.T) {
	if !isPublishedInitialAdminHash(legacyInitialAdminHash) {
		t.Fatal("legacy seed hash was not recognized")
	}
	for _, password := range []string{"123456", "请设置8-64位强密码", "replace-with-a-unique-strong-bootstrap-password", "Sandbox-Custom-Secret-42!"} {
		hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
		if err != nil {
			t.Fatal(err)
		}
		want := password != "Sandbox-Custom-Secret-42!"
		if got := isPublishedInitialAdminHash(string(hash)); got != want {
			t.Fatalf("published password classification = %v, want %v", got, want)
		}
	}
	if isPublishedInitialAdminHash("not-a-bcrypt-hash") {
		t.Fatal("an invalid hash must not be treated as a known bootstrap credential")
	}
}

func TestInitialAdminPasswordPreservesConfiguredWhitespace(t *testing.T) {
	want := "  StrongPass123!  "
	password, err := initialAdminPassword(config.Config{
		App:  config.AppConfig{Env: "production"},
		Auth: config.AuthConfig{InitialAdminPassword: want},
	})
	if err != nil {
		t.Fatalf("initialAdminPassword() error = %v", err)
	}
	if password != want {
		t.Fatalf("configured password = %q, want exact input", password)
	}
}
