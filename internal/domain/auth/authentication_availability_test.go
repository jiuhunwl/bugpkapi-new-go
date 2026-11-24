package auth

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"bugpk-api/internal/config"
	apperrors "bugpk-api/internal/pkg/errors"

	"github.com/cloudwego/hertz/pkg/protocol/consts"
	_ "github.com/go-sql-driver/mysql"
)

func TestCodesReturnsServiceUnavailableWhenUserLookupFails(t *testing.T) {
	db, err := sql.Open("mysql", "root:root@tcp(127.0.0.1:1)/bugpk_api_test?timeout=50ms&readTimeout=50ms&writeTimeout=50ms")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	cfg := config.AuthConfig{
		AccessTokenTTL: time.Minute,
		Issuer:         "bugpk-auth-availability-test",
		Secret:         strings.Repeat("a", 64),
	}
	jwtService, err := NewJWTService(cfg)
	if err != nil {
		t.Fatal(err)
	}
	token, err := jwtService.CreateAccessToken(User{ID: 1, Username: "admin", TokenVersion: 1}, time.Now())
	if err != nil {
		t.Fatal(err)
	}

	service := &Service{repo: NewRepository(db), jwt: jwtService}
	_, err = service.Codes(t.Context(), token)
	assertAuthenticationUnavailable(t, err)
}

func assertAuthenticationUnavailable(t *testing.T, err error) {
	t.Helper()
	business, ok := apperrors.AsBusinessError(err)
	if !ok || business.StatusCode != consts.StatusServiceUnavailable {
		t.Fatalf("authentication error = %v, want service unavailable", err)
	}
}
