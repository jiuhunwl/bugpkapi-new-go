package observability

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"bugpk-api/internal/domain/auth"
	"bugpk-api/internal/domain/rbac"
	hertzapp "github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
)

func TestOperationAuthSafeSummariesDirectAndHTTP(t *testing.T) {
	cases := []struct {
		name, action, summary, want string
		success                     bool
		httpStatus                  int
	}{
		{"password error with 429", "login", "登录失败：账号或密码错误", "登录失败：账号或密码错误", false, 429},
		{"password error", "login", "登录失败：账号或密码错误", "登录失败：账号或密码错误", false, 403},
		{"success", "login", "登录成功", "登录成功", true, 200},
		{"quick login", "login", "快捷登录成功", "快捷登录成功", true, 302},
		{"disabled reason", "login", "禁用账号", "禁用账号", false, 403},
		{"disabled fixed message", "login", "登录被拒绝：账号已被禁用", "登录被拒绝：账号已被禁用", false, 403},
		{"logout success", "logout", "退出登录", "退出登录成功", true, 200},
		{"logout failure", "logout", "退出登录", "退出登录失败", false, 503},
		{"empty login success", "login", "", "登录成功", true, 200},
		{"empty login failure", "login", "", "登录失败", false, 403},
		{"empty logout success", "logout", "", "退出登录成功", true, 200},
		{"empty logout failure", "logout", "", "退出登录失败", false, 500},
		{"unknown sensitive summary", "login", "secret-summary password=abc", "登录失败", false, 403},
		{"prefix is not whitelist", "login", "登录失败：账号或密码错误 secret-summary", "登录失败", false, 403},
		{"success text cannot describe failure", "login", "登录成功", "登录失败", false, 403},
		{"failed text cannot describe success", "login", "登录失败：账号或密码错误", "登录成功", true, 200},
		{"wrong action text", "logout", "快捷登录成功", "退出登录成功", true, 200},
	}
	for _, tc := range cases {
		for _, http := range []bool{false, true} {
			t.Run(tc.name+fmt.Sprint(http), func(t *testing.T) {
				repo, state := auditTestRepo(t)
				svc := NewService(repo, nil)
				adapter := NewAuthAuditAdapter(svc)
				entry := auth.AuditEntry{UserID: 42, Username: "canonical", Roles: []string{"admin"}, Action: tc.action, Summary: tc.summary, Status: tc.success, StatusCode: tc.httpStatus, ErrorMessage: "secret-error", UserAgent: "secret-agent"}
				if http {
					h := server.New()
					g := h.Group("/system-api")
					g.Use(svc.OperationAuditMiddleware())
					g.POST("/auth/"+tc.action, func(ctx context.Context, c *hertzapp.RequestContext) {
						adapter.RecordAuth(ctx, entry)
						c.JSON(tc.httpStatus, map[string]string{"password": "secret-body"})
					})
					r := ut.PerformRequest(h.Engine, "POST", "/system-api/auth/"+tc.action+"?token=secret-query", nil)
					if r.Code != tc.httpStatus {
						t.Fatalf("HTTP outcome changed: %d", r.Code)
					}
				} else {
					adapter.RecordAuth(context.Background(), entry)
				}
				if state.writes != 1 || state.args[4].Value != tc.want || state.args[11].Value != int64(tc.httpStatus) {
					t.Fatalf("summary/outcome lost: %v; want %q", state.args, tc.want)
				}
				if strings.Contains(fmt.Sprint(state.args), "secret-") {
					t.Fatalf("untrusted content leaked: %v", state.args)
				}
			})
		}
	}
}

func TestOperationAuthSafeSummaryHonorsFinalHTTPFailure(t *testing.T) {
	repo, state := auditTestRepo(t)
	svc := NewService(repo, nil)
	adapter := NewAuthAuditAdapter(svc)
	h := server.New()
	g := h.Group("/system-api")
	g.Use(svc.OperationAuditMiddleware())
	g.POST("/auth/login", func(ctx context.Context, c *hertzapp.RequestContext) {
		adapter.RecordAuth(ctx, auth.AuditEntry{UserID: 42, Username: "canonical", Action: "login", Summary: "快捷登录成功", Status: true})
		c.JSON(503, map[string]string{"token": "secret-body"})
	})
	ut.PerformRequest(h.Engine, "POST", "/system-api/auth/login", nil)
	if state.writes != 1 || state.args[4].Value != "登录失败" || state.args[9].Value != int64(0) || state.args[11].Value != int64(503) {
		t.Fatalf("misleading auth success: %v", state.args)
	}
}

func TestOperationAuditKnownNumericTargetParams(t *testing.T) {
	for _, tc := range []struct{ route, path, want string }{
		{"/tickets/quick-replies/:quickReplyId", "/tickets/quick-replies/7", "7"},
		{"/tickets/attachments/:attachmentId", "/tickets/attachments/8", "8"},
		{"/tickets/:id/attachments/:attachmentId", "/tickets/42/attachments/8", "42"},
		{"/tickets/quick-replies/:quickReplyId", "/tickets/quick-replies/secret-param", ""},
		{"/tickets/quick-replies/:quickReplyId", "/tickets/quick-replies/" + strings.Repeat("9", 65), ""},
		{"/tickets/:unknown", "/tickets/secret-param", ""},
	} {
		t.Run(tc.path, func(t *testing.T) {
			repo, state := auditTestRepo(t)
			svc := NewService(repo, nil)
			h := server.New()
			g := h.Group("/system-api")
			g.Use(auditActor(rbac.Principal{ID: 42, Username: "canonical", Roles: []string{"admin"}}), svc.OperationAuditMiddleware())
			g.PUT(tc.route, func(_ context.Context, c *hertzapp.RequestContext) { c.SetStatusCode(204) })
			ut.PerformRequest(h.Engine, "PUT", "/system-api"+tc.path, nil)
			if state.writes != 1 || state.args[8].Value != tc.want || strings.Contains(fmt.Sprint(state.args), "secret-param") {
				t.Fatalf("unsafe/missing target: %v; want %q", state.args, tc.want)
			}
		})
	}
}
