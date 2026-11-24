package user

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"bugpk-api/internal/config"
	"bugpk-api/internal/domain/auth"
	apperrors "bugpk-api/internal/pkg/errors"

	"github.com/cloudwego/hertz/pkg/protocol/consts"
	_ "github.com/go-sql-driver/mysql"
)

func TestAuthenticateReturnsServiceUnavailableWhenUserLookupFails(t *testing.T) {
	db, err := sql.Open("mysql", "root:root@tcp(127.0.0.1:1)/bugpk_api_test?timeout=50ms&readTimeout=50ms&writeTimeout=50ms")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	cfg := config.AuthConfig{
		AccessTokenTTL: time.Minute,
		Issuer:         "bugpk-user-availability-test",
		Secret:         strings.Repeat("u", 64),
	}
	jwtService, err := auth.NewJWTService(cfg)
	if err != nil {
		t.Fatal(err)
	}
	token, err := jwtService.CreateAccessToken(auth.User{ID: 1, Username: "user", TokenVersion: 1}, time.Now())
	if err != nil {
		t.Fatal(err)
	}

	service := &Service{repo: NewRepository(db), jwt: jwtService}
	_, _, err = service.Authenticate(t.Context(), token)
	business, ok := apperrors.AsBusinessError(err)
	if !ok || business.StatusCode != consts.StatusServiceUnavailable {
		t.Fatalf("Authenticate() error = %v, want service unavailable", err)
	}
}
