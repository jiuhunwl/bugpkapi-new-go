package observability

import (
	"bugpk-api/internal/config"
	"bugpk-api/internal/domain/auth"
	"bugpk-api/internal/domain/rbac"
	"context"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	hertzapp "github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"strings"
	"testing"
	"time"
)

func auditJWT(t *testing.T) (*auth.JWTService, string) {
	t.Helper()
	j, err := auth.NewJWTService(config.AuthConfig{Secret: strings.Repeat("a", 64), Issuer: "audit-http", AccessTokenTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	token, err := j.CreateAccessToken(auth.User{ID: 42, Username: "canonical", TokenVersion: 1}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return j, token
}
func TestOperationMineHTTPContractAndAdminPermissions(t *testing.T) {
	repo, state := auditTestRepo(t)
	j, token := auditJWT(t)
	auditQueries := 0
	state.query = func(q string, args []driver.NamedValue) (driver.Rows, error) {
		switch {
		case strings.Contains(q, "from bp_user"):
			return auditRows([]driver.Value{int64(42), "canonical", int64(1), int64(0), "", "", int64(1)}), nil
		case strings.Contains(q, "from bp_role"):
			return auditRows([]driver.Value{"user"}), nil
		case strings.Contains(q, "from bp_menu"):
			return auditRows(), nil
		case strings.Contains(q, "bp_operation_log"):
			auditQueries++
			if !strings.Contains(q, "l.actor_kind in ('admin','user')") || !strings.Contains(q, "l.user_id = ?") || args[0].Value != int64(42) || strings.Contains(q, "l.username like") {
				t.Fatalf("HTTP scope spoofed: %s %v", q, args)
			}
			if strings.Contains(q, "count(*)") {
				return auditRows([]driver.Value{int64(1)}), nil
			}
			return auditRows([]driver.Value{int64(7), int64(42), "canonical", "user", "password", "changed", "PUT", "/system-api/user/password", "", "", int64(0), "secret-old-error", int64(400), "203.0.113.1", "", "", "", "secret-user-agent", int64(1), "secret-old-detail", "2026-10-03 01:00:00"}), nil
		default:
			return nil, fmt.Errorf("unexpected query %s", q)
		}
	}
	svc := NewService(repo, rbac.NewService(rbac.NewRepository(repo.db), j))
	h := server.New()
	NewHandler(svc).RegisterOperationLog(h.Group("/system-api/system"))
	resp := ut.PerformRequest(h.Engine, "GET", "/system-api/system/operation-log/mine?userId=999&username=admin", nil, ut.Header{Key: "Authorization", Value: "Bearer " + token})
	if resp.Code != 200 || auditQueries != 2 {
		t.Fatalf("mine status=%d queries=%d body=%s", resp.Code, auditQueries, resp.Body.String())
	}
	var payload struct{ Data OperationLogPage }
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Data.Total != 1 || payload.Data.Page != 1 || len(payload.Data.Items) != 1 || payload.Data.Items[0].UserID != "42" {
		t.Fatalf("wrong page shape: %s", resp.Body.String())
	}
	if strings.Contains(resp.Body.String(), "secret-") || payload.Data.Items[0].ClientIP != "203.0.113.*" {
		t.Fatalf("private data leak: %s", resp.Body.String())
	}
	for _, path := range []string{"list", "overview"} {
		r := ut.PerformRequest(h.Engine, "GET", "/system-api/system/operation-log/"+path, nil, ut.Header{Key: "Authorization", Value: "Bearer " + token})
		if r.Code != 403 {
			t.Fatalf("ordinary user got %d for %s: %s", r.Code, path, r.Body.String())
		}
	}
	r := ut.PerformRequest(h.Engine, "GET", "/system-api/system/operation-log/mine?userId=not-a-number&username=someone", nil, ut.Header{Key: "Authorization", Value: "Bearer " + token})
	if r.Code != 200 {
		t.Fatalf("personal identity fields must be ignored, got %d", r.Code)
	}
	r = ut.PerformRequest(h.Engine, "GET", "/system-api/system/operation-log/mine", nil)
	if r.Code != 401 || auditQueries != 4 {
		t.Fatalf("anonymous mine status=%d queries=%d", r.Code, auditQueries)
	}
}
func TestOperationAuditSnapshotBeforeSelfMutationAndNoStaleToken(t *testing.T) {
	repo, state := auditTestRepo(t)
	j, token := auditJWT(t)
	mutated := false
	state.query = func(q string, args []driver.NamedValue) (driver.Rows, error) {
		if strings.Contains(q, "from bp_user") {
			version := int64(1)
			if mutated {
				version = 2
			}
			return auditRows([]driver.Value{int64(42), "canonical", int64(1), int64(0), "", "", version}), nil
		}
		if strings.Contains(q, "from bp_role") {
			role := "admin"
			if mutated {
				role = "user"
			}
			return auditRows([]driver.Value{role}), nil
		}
		return nil, fmt.Errorf("unexpected query %s", q)
	}
	svc := NewService(repo, rbac.NewService(rbac.NewRepository(repo.db), j))
	h := server.New()
	g := h.Group("/system-api")
	g.Use(svc.OperationAuditMiddleware())
	g.PUT("/user/password", func(_ context.Context, c *hertzapp.RequestContext) { mutated = true; c.SetStatusCode(204) })
	ut.PerformRequest(h.Engine, "PUT", "/system-api/user/password", nil, ut.Header{Key: "Authorization", Value: "Bearer " + token})
	if state.writes != 1 || state.args[0].Value != int64(42) || state.args[20].Value != "admin" {
		t.Fatalf("self mutation erased event-time actor: %v", state.args)
	}
	ut.PerformRequest(h.Engine, "PUT", "/system-api/user/password", nil, ut.Header{Key: "Authorization", Value: "Bearer " + token})
	if state.writes != 1 {
		t.Fatal("revoked token reused stale audit identity")
	}
}
func TestOperationAuditTrustedGETLoginRedirectMergesOnce(t *testing.T) {
	repo, state := auditTestRepo(t)
	svc := NewService(repo, nil)
	h := server.New()
	g := h.Group("/system-api")
	g.Use(svc.OperationAuditMiddleware())
	adapter := NewAuthAuditAdapter(svc)
	g.GET("/auth/qq/callback", func(ctx context.Context, c *hertzapp.RequestContext) {
		adapter.RecordAuth(ctx, auth.AuditEntry{UserID: 42, Username: "canonical", Roles: []string{"admin"}, Action: "login", Status: true})
		c.Redirect(302, []byte("/console"))
	})
	ut.PerformRequest(h.Engine, "GET", "/system-api/auth/qq/callback?code=secret-token", nil)
	if state.writes != 1 || state.args[11].Value != int64(302) || state.args[9].Value != int64(1) || !strings.Contains(fmt.Sprint(state.args[4].Value), "成功") || strings.Contains(fmt.Sprint(state.args), "secret-") {
		t.Fatalf("GET domain login outcome lost: %v", state.args)
	}
}

func TestOperationAuditConfiguredPrefixAndSafeTarget(t *testing.T) {
	repo, state := auditTestRepo(t)
	svc := NewService(repo, nil)
	h := server.New()
	g := h.Group("/internal/api")
	g.Use(auditActor(rbac.Principal{ID: 42, Username: "canonical", Roles: []string{"admin"}}), svc.OperationAuditMiddleware("/internal/api"))
	g.PUT("/interface/:id", func(_ context.Context, c *hertzapp.RequestContext) { c.SetStatusCode(200) })
	ut.PerformRequest(h.Engine, "PUT", "/internal/api/interface/7?key=secret-query", nil)
	if state.writes != 1 || state.args[2].Value != "interface" || state.args[8].Value != "7" {
		t.Fatalf("custom prefix/target lost: %v", state.args)
	}
	ut.PerformRequest(h.Engine, "PUT", "/internal/api/interface/secret-path-token", nil)
	if state.writes != 2 || strings.Contains(fmt.Sprint(state.args), "secret-") {
		t.Fatalf("dynamic path leaked: %v", state.args)
	}
}
