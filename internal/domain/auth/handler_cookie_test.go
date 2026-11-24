package auth

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"bugpk-api/internal/config"
	apperrors "bugpk-api/internal/pkg/errors"

	hertzapp "github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"github.com/redis/go-redis/v9"
)

func TestRefreshCookieIsOnlyClearedForUnauthorizedErrors(t *testing.T) {
	unauthorized := apperrors.New(consts.StatusUnauthorized, "unauthorized")
	tooMany := apperrors.New(consts.StatusTooManyRequests, "too many")
	unavailable := apperrors.New(consts.StatusServiceUnavailable, "unavailable")
	if !shouldClearRefreshCookie(unauthorized) {
		t.Fatal("unauthorized refresh error did not clear the stale cookie")
	}
	for _, err := range []error{tooMany, unavailable, errors.New("internal failure")} {
		if shouldClearRefreshCookie(err) {
			t.Fatalf("transient refresh error cleared the cookie: %v", err)
		}
	}
}

func TestRefreshCookieUsesAuthPathAndClearsLegacyPath(t *testing.T) {
	cfg := config.Default()
	cfg.Auth.RefreshCookieSecure = true
	cfg.Auth.RefreshCookieSameSite = "Strict"
	handler := &Handler{cfg: cfg}
	ctx := hertzapp.NewContext(0)

	handler.setRefreshCookie(ctx, "refresh-token", 3600)
	headers := cookieHeaderStrings(ctx)
	if len(headers) != 2 {
		t.Fatalf("Set-Cookie headers = %v, want current and legacy paths", headers)
	}
	current := findCookieHeader(headers, "Path=/system-api/auth")
	for _, fragment := range []string{"refresh_token=refresh-token", "max-age=3600", "httponly", "secure", "samesite=strict"} {
		if !strings.Contains(strings.ToLower(current), fragment) {
			t.Fatalf("current refresh cookie missing %q: %q", fragment, current)
		}
	}
	legacy := findCookieHeader(headers, "Path=/system-api;")
	if !strings.Contains(strings.ToLower(legacy), "max-age=0") || strings.Contains(legacy, "refresh-token") {
		t.Fatalf("legacy refresh cookie was not deleted: %q", legacy)
	}
}

func TestClearRefreshCookieDeletesCurrentAndLegacyPaths(t *testing.T) {
	handler := &Handler{cfg: config.Default()}
	ctx := hertzapp.NewContext(0)

	handler.clearRefreshCookie(ctx)
	headers := cookieHeaderStrings(ctx)
	for _, path := range []string{"Path=/system-api/auth", "Path=/system-api;"} {
		header := findCookieHeader(headers, path)
		if !strings.Contains(strings.ToLower(header), "max-age=0") {
			t.Fatalf("cookie path %q was not expired: %q", path, header)
		}
	}
}

func TestLogoutClearsCookieWhenServerRevocationFails(t *testing.T) {
	cfg := config.Default()
	jwtService, err := NewJWTService(cfg.Auth)
	if err != nil {
		t.Fatal(err)
	}
	userAgent := "BugPk-Api cookie test"
	token, _, _, err := jwtService.CreateRefreshToken(User{
		ID: 1, Username: "admin", Status: 1, TokenVersion: 1,
	}, DeviceFingerprint("", userAgent), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	rdb := redis.NewClient(&redis.Options{
		Dialer: func(context.Context, string, string) (net.Conn, error) {
			return nil, errors.New("redis offline")
		},
		DialTimeout: 50 * time.Millisecond,
		MaxRetries:  -1,
	})
	defer rdb.Close()
	handler := &Handler{
		cfg: cfg,
		service: &Service{
			jwt: jwtService,
			rdb: rdb,
		},
	}
	ctx := hertzapp.NewContext(0)
	ctx.Request.Header.Set("Cookie", cfg.Auth.RefreshCookieName+"="+token)
	ctx.Request.Header.Set("User-Agent", userAgent)

	handler.logout(context.Background(), ctx)
	if ctx.Response.StatusCode() != consts.StatusServiceUnavailable {
		t.Fatalf("logout status = %d, want %d", ctx.Response.StatusCode(), consts.StatusServiceUnavailable)
	}
	for _, header := range cookieHeaderStrings(ctx) {
		if !strings.Contains(strings.ToLower(header), "max-age=0") {
			t.Fatalf("logout retained refresh cookie after revocation failure: %q", header)
		}
	}
}

func TestCookieAuthRejectsCrossSiteWithoutClearingSession(t *testing.T) {
	handler := &Handler{cfg: config.Default()}
	for name, invoke := range map[string]func(context.Context, *hertzapp.RequestContext){
		"refresh": handler.refresh,
		"logout":  handler.logout,
	} {
		t.Run(name, func(t *testing.T) {
			ctx := hertzapp.NewContext(0)
			ctx.Request.Header.Set("Sec-Fetch-Site", "cross-site")
			invoke(context.Background(), ctx)
			if ctx.Response.StatusCode() != consts.StatusForbidden {
				t.Fatalf("status=%d, want 403", ctx.Response.StatusCode())
			}
			if headers := cookieHeaderStrings(ctx); len(headers) != 0 {
				t.Fatalf("cross-site rejection modified refresh cookie: %v", headers)
			}
		})
	}
}

func TestCookieAuthAllowsSameOriginAndNonBrowserRequests(t *testing.T) {
	for _, value := range []string{"", "none", "same-origin", "SAME-ORIGIN"} {
		ctx := hertzapp.NewContext(0)
		if value != "" {
			ctx.Request.Header.Set("Sec-Fetch-Site", value)
		}
		if err := requireSameOriginCookieRequest(ctx); err != nil {
			t.Fatalf("Sec-Fetch-Site %q was rejected: %v", value, err)
		}
	}
	for _, value := range []string{"same-site", "cross-site", "unexpected"} {
		ctx := hertzapp.NewContext(0)
		ctx.Request.Header.Set("Sec-Fetch-Site", value)
		if err := requireSameOriginCookieRequest(ctx); err == nil {
			t.Fatalf("Sec-Fetch-Site %q was accepted", value)
		}
	}
}

func TestCookieAuthValidatesOriginWhenFetchMetadataIsMissing(t *testing.T) {
	for _, test := range []struct {
		headers map[string]string
		name    string
		wantErr bool
	}{
		{name: "same origin", headers: map[string]string{"Host": "app.example.com", "Origin": "https://app.example.com"}},
		{name: "same origin referer", headers: map[string]string{"Host": "app.example.com", "Referer": "https://app.example.com/profile"}},
		{name: "cross origin", wantErr: true, headers: map[string]string{"Host": "app.example.com", "Origin": "https://evil.example"}},
		{name: "null origin", wantErr: true, headers: map[string]string{"Host": "app.example.com", "Origin": "null"}},
		{name: "userinfo origin", wantErr: true, headers: map[string]string{"Host": "app.example.com", "Origin": "https://app.example.com@evil.example"}},
		{name: "non browser", headers: map[string]string{"Host": "app.example.com"}},
		{name: "trusted fetch metadata survives proxy host rewrite", headers: map[string]string{"Host": "backend:8080", "Origin": "http://localhost:5555", "Sec-Fetch-Site": "same-origin"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := hertzapp.NewContext(0)
			for key, value := range test.headers {
				ctx.Request.Header.Set(key, value)
			}
			err := requireSameOriginCookieRequest(ctx)
			if (err != nil) != test.wantErr {
				t.Fatalf("error=%v, wantErr=%v", err, test.wantErr)
			}
		})
	}
}

func TestRefreshCookieValuesKeepsDuplicateCookieCandidates(t *testing.T) {
	ctx := hertzapp.NewContext(0)
	ctx.Request.Header.Set("Cookie", "other=x; refresh_token=legacy%2Dtoken; refresh_token=active-token")
	values := refreshCookieValues(ctx, "refresh_token")
	if len(values) != 2 || values[0] != "legacy-token" || values[1] != "active-token" {
		t.Fatalf("refresh cookie candidates = %#v", values)
	}
}

func cookieHeaderStrings(ctx *hertzapp.RequestContext) []string {
	result := make([]string, 0, 2)
	ctx.Response.Header.VisitAllCookie(func(_, value []byte) {
		result = append(result, string(value))
	})
	return result
}

func findCookieHeader(headers []string, fragment string) string {
	for _, header := range headers {
		if strings.Contains(strings.ToLower(header), strings.ToLower(fragment)) {
			return header
		}
	}
	return ""
}
