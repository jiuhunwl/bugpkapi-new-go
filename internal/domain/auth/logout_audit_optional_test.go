package auth

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"bugpk-api/internal/config"
	hertzapp "github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"github.com/redis/go-redis/v9"
)

type optionalLogoutAuditRecorder struct{ calls int }

func (r *optionalLogoutAuditRecorder) RecordAuth(context.Context, AuditEntry) { r.calls++ }

func TestLogoutOptionalAuditDependenciesDoNotBlockCookieCleanup(t *testing.T) {
	for _, name := range []string{"audit disabled with unusable identity repository", "audit enabled without repository"} {
		t.Run(name, func(t *testing.T) {
			cfg := config.Default()
			jwtService, err := NewJWTService(cfg.Auth)
			if err != nil {
				t.Fatal(err)
			}
			userAgent := "logout optional audit test"
			token, _, _, err := jwtService.CreateRefreshToken(User{ID: 1, Username: "admin", Status: 1, TokenVersion: 1}, DeviceFingerprint("", userAgent), time.Now())
			if err != nil {
				t.Fatal(err)
			}
			rdb := redis.NewClient(&redis.Options{Dialer: func(context.Context, string, string) (net.Conn, error) { return nil, errors.New("redis offline") }, DialTimeout: 50 * time.Millisecond, MaxRetries: -1})
			defer rdb.Close()
			recorder := &optionalLogoutAuditRecorder{}
			service := &Service{jwt: jwtService, rdb: rdb}
			if strings.HasPrefix(name, "audit disabled") {
				service.repo = &Repository{}
			} else {
				service.audit = recorder
			}
			handler := &Handler{cfg: cfg, service: service}
			ctx := hertzapp.NewContext(0)
			ctx.Request.Header.Set("Cookie", cfg.Auth.RefreshCookieName+"="+token)
			ctx.Request.Header.Set("User-Agent", userAgent)
			handler.logout(context.Background(), ctx)
			if ctx.Response.StatusCode() != consts.StatusServiceUnavailable {
				t.Fatalf("revocation outcome changed: %d", ctx.Response.StatusCode())
			}
			headers := cookieHeaderStrings(ctx)
			if len(headers) != 2 {
				t.Fatalf("expected both cookie paths to clear, got %v", headers)
			}
			for _, header := range headers {
				if !strings.Contains(strings.ToLower(header), "max-age=0") {
					t.Fatalf("cookie retained after revocation failure: %q", header)
				}
			}
			if recorder.calls != 0 {
				t.Fatal("unattributed optional audit was recorded")
			}
		})
	}
}
