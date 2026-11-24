package observability

import (
	"bugpk-api/internal/domain/auth"
	"bugpk-api/internal/domain/rbac"
	"context"
	"fmt"
	hertzapp "github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"strings"
	"testing"
)

func auditActor(p rbac.Principal) hertzapp.HandlerFunc {
	return func(ctx context.Context, c *hertzapp.RequestContext) { c.Next(rbac.WithPrincipal(ctx, p)) }
}

func TestOperationAuditEligibilityAndPrivacy(t *testing.T) {
	for _, tc := range []struct {
		method, path   string
		authenticated  bool
		status, writes int
	}{
		{"POST", "/system/user", false, 200, 0}, {"DELETE", "/key/7", false, 403, 0},
		{"POST", "/system/user", true, 201, 1}, {"PUT", "/user/password", true, 400, 1},
		{"PATCH", "/system/role/7/status", true, 403, 1}, {"DELETE", "/interface/7", true, 500, 1},
		{"GET", "/interface/call-log/export", true, 200, 1}, {"GET", "/key/7/secret", true, 200, 1},
		{"GET", "/user/info", true, 200, 0}, {"POST", "/friend-links/apply", true, 200, 0},
		{"POST", "/auth/refresh", true, 200, 0}, {"POST", "/auth/register", true, 200, 0},
		{"POST", "/auth/captcha/7/verify", true, 200, 0}, {"POST", "/auth/password-reset", true, 200, 0},
		{"POST", "/payment/alipay/notify", true, 200, 0}, {"POST", "/payment/hupijiao/notify/alipay", true, 200, 0},
		{"POST", "/payment/alipay/config/validate", true, 200, 0}, {"POST", "/payment/recharge/orders/7/sync", true, 200, 0},
		{"POST", "/tickets/7/read", true, 200, 0}, {"POST", "/interface/7/invoke", true, 200, 0},
		{"POST", "/interface/7/upstreams/check", true, 200, 0}, {"POST", "/interface/proxy-pool/inspect", true, 200, 0},
		{"POST", "/interface/proxy-pool/test", true, 200, 0}, {"POST", "/system/ip-config/test-source", true, 200, 0},
		{"POST", "/system/register/email-config/test", true, 200, 0}, {"POST", "/user/send-bind-email-code", true, 200, 0},
		{"PUT", "/friend-links/admin/applications/7/approve", true, 200, 1},
		{"PUT", "/system/user/7", true, 204, 1}, {"POST", "/payment/orders/7/sync", true, 200, 1},
		{"POST", "/unregistered/probe", true, 404, 0},
	} {
		t.Run(tc.method+tc.path+fmt.Sprint(tc.authenticated), func(t *testing.T) {
			repo, state := auditTestRepo(t)
			svc := NewService(repo, nil)
			h := server.New()
			g := h.Group("/system-api")
			if tc.authenticated {
				g.Use(auditActor(rbac.Principal{ID: 42, Username: "canonical", Roles: []string{"admin"}}))
			}
			g.Use(svc.OperationAuditMiddleware())
			g.Handle(tc.method, tc.path, func(_ context.Context, c *hertzapp.RequestContext) {
				c.JSON(tc.status, map[string]string{"password": "secret-password", "token": "secret-token", "url": "https://secret.example/?key=abc"})
			})
			resp := ut.PerformRequest(h.Engine, tc.method, "/system-api"+tc.path+"?token=secret-query", nil)
			if resp.Code != tc.status || state.writes != tc.writes {
				t.Fatalf("status=%d writes=%d want %d/%d", resp.Code, state.writes, tc.status, tc.writes)
			}
			if state.writes > 0 {
				if strings.Contains(fmt.Sprint(state.args), "secret-") {
					t.Fatalf("secret leaked: %v", state.args)
				}
				if state.args[11].Value != int64(tc.status) {
					t.Fatal("HTTP outcome lost")
				}
			}
		})
	}
}
func TestOperationAuthAdapterRejectsNoise(t *testing.T) {
	for _, entry := range []auth.AuditEntry{{Action: "login", Username: "unknown"}, {UserID: 42, Action: "register", Status: true}, {UserID: 42, Action: "refresh", Status: true}} {
		repo, state := auditTestRepo(t)
		NewAuthAuditAdapter(NewService(repo, nil)).RecordAuth(context.Background(), entry)
		if state.writes != 0 {
			t.Fatalf("noise persisted: %+v", entry)
		}
	}
}
func TestOperationAuditAuthEventUsesFinalHTTPOutcome(t *testing.T) {
	repo, state := auditTestRepo(t)
	svc := NewService(repo, nil)
	adapter := NewAuthAuditAdapter(svc)
	h := server.New()
	g := h.Group("/system-api")
	g.Use(svc.OperationAuditMiddleware())
	g.POST("/auth/login", func(ctx context.Context, c *hertzapp.RequestContext) {
		adapter.RecordAuth(ctx, auth.AuditEntry{UserID: 42, Username: "canonical", Action: "login", Status: true, ErrorMessage: "secret-domain-body"})
		c.JSON(503, map[string]string{"token": "secret-body"})
	})
	ut.PerformRequest(h.Engine, "POST", "/system-api/auth/login", nil)
	if state.writes != 1 || state.args[9].Value != int64(0) || state.args[11].Value != int64(503) || strings.Contains(fmt.Sprint(state.args), "secret-") {
		t.Fatalf("bad outcome or private data: %+v", state.args)
	}
}
