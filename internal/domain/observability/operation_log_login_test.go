package observability

import (
	"bugpk-api/internal/config"
	"bugpk-api/internal/domain/auth"
	"bugpk-api/internal/pkg/response"
	"context"
	"database/sql/driver"
	"fmt"
	hertzapp "github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"github.com/redis/go-redis/v9"
	"golang.org/x/crypto/bcrypt"
	"net"
	"strings"
	"testing"
	"time"
)

type auditRedisHook struct {
	locked     bool
	increments int
}

func (h *auditRedisHook) DialHook(next redis.DialHook) redis.DialHook {
	return func(context.Context, string, string) (net.Conn, error) { return nil, fmt.Errorf("unexpected network") }
}
func (h *auditRedisHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(_ context.Context, cmd redis.Cmder) error {
		switch c := cmd.(type) {
		case *redis.StringCmd:
			if h.locked && strings.Contains(fmt.Sprint(cmd.Args()), "lock:account:") {
				c.SetVal("1")
				return nil
			}
			c.SetErr(redis.Nil)
			return redis.Nil
		case *redis.Cmd:
			h.increments++
			c.SetVal(int64(1))
		case *redis.IntCmd:
			c.SetVal(1)
		case *redis.StatusCmd:
			c.SetVal("OK")
		default:
			return fmt.Errorf("unexpected Redis command %T", cmd)
		}
		return nil
	}
}
func (h *auditRedisHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []redis.Cmder) error {
		for _, cmd := range cmds {
			if err := h.ProcessHook(nil)(ctx, cmd); err != nil {
				return err
			}
		}
		return nil
	}
}
func TestOperationLoginCanonicalIdentityAndSecurity(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("correct-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, account, password, role string
		known, disabled, locked       bool
		status, writes                int
	}{
		{"email password error", " admin@example.com ", "wrong-password", "admin", true, false, false, 403, 1},
		{"username password error", "canonical", "wrong-password", "user", true, false, false, 403, 1},
		{"unknown", "unknown@example.com", "wrong-password", "", false, false, false, 403, 0},
		{"admin success", "admin@example.com", "correct-password", "admin", true, false, false, 200, 1},
		{"user success", "canonical", "correct-password", "user", true, false, false, 200, 1},
		{"disabled", "canonical", "correct-password", "admin", true, true, false, 403, 0},
		{"limited", "canonical", "wrong-password", "admin", true, false, true, 429, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, state := auditTestRepo(t)
			state.query = func(q string, args []driver.NamedValue) (driver.Rows, error) {
				switch {
				case strings.Contains(q, "bp_captcha_global_config"):
					return auditRows([]driver.Value{int64(0)}), nil
				case strings.Contains(q, "from bp_user"):
					if args[0].Value != strings.TrimSpace(tc.account) {
						t.Fatalf("account lookup changed: %v", args)
					}
					if !tc.known {
						return &operationRows{width: 13}, nil
					}
					status := int64(1)
					if tc.disabled {
						status = 0
					}
					// 列序与 scanUser 一致：id, username, password_hash,
					// display_name, email, mobile, status, ban_permanent,
					// ban_until, ban_reason, token_version, totp_enabled,
					// totp_secret（未启用 2FA → 0/''）。
					return auditRows([]driver.Value{int64(42), "canonical", string(hash), "Canonical", "admin@example.com", nil, status, int64(1), "", "", int64(0), int64(0), ""}), nil
				case strings.Contains(q, "from bp_role"):
					return auditRows([]driver.Value{tc.role}), nil
				default:
					return nil, fmt.Errorf("unexpected query: %s", q)
				}
			}
			hook := &auditRedisHook{locked: tc.locked}
			rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", MaxRetries: -1})
			rdb.AddHook(hook)
			t.Cleanup(func() { rdb.Close() })
			cfg := config.AuthConfig{Secret: strings.Repeat("a", 64), Issuer: "audit-test", AccessTokenTTL: time.Hour, RefreshTokenTTL: 24 * time.Hour}
			jwt, err := auth.NewJWTService(cfg)
			if err != nil {
				t.Fatal(err)
			}
			authRepo := auth.NewRepository(repo.db)
			captcha := auth.NewCaptchaService(config.CaptchaConfig{}, authRepo, rdb)
			svc := auth.NewService(cfg, authRepo, captcha, jwt, auth.NewRateLimiter(rdb, cfg.Secret), rdb, nil)
			auditSvc := NewService(repo, nil)
			svc.SetAuditRecorder(NewAuthAuditAdapter(auditSvc))
			h := server.New()
			g := h.Group("/system-api")
			g.Use(auditSvc.OperationAuditMiddleware())
			g.POST("/auth/login", func(ctx context.Context, c *hertzapp.RequestContext) {
				pair, err := svc.Login(ctx, auth.LoginRequest{Username: tc.account, Password: tc.password}, "binding", "203.0.113.1", "test-agent")
				if err != nil {
					response.FromError(c, err)
					return
				}
				response.OK(c, pair)
			})
			resp := ut.PerformRequest(h.Engine, "POST", "/system-api/auth/login", nil)
			if resp.Code != tc.status || state.writes != tc.writes {
				t.Fatalf("status=%d writes=%d want %d/%d", resp.Code, state.writes, tc.status, tc.writes)
			}
			if tc.password == "wrong-password" && !tc.locked && hook.increments != 3 {
				t.Fatalf("login failure limiter changed: %d", hook.increments)
			}
			if state.writes > 0 {
				if state.args[0].Value != int64(42) || state.args[1].Value != "canonical" || state.args[11].Value != int64(tc.status) || state.args[20].Value != tc.role {
					t.Fatalf("identity/role/outcome wrong: %v", state.args)
				}
				if strings.Contains(fmt.Sprint(state.args), tc.password) || strings.Contains(fmt.Sprint(state.args), "example.com") {
					t.Fatal("credentials or submitted email leaked")
				}
			}
		})
	}
}

func TestOperationLogoutUsesCanonicalLiveRoleSnapshot(t *testing.T) {
	repo, state := auditTestRepo(t)
	state.query = func(q string, args []driver.NamedValue) (driver.Rows, error) {
		if strings.Contains(q, "from bp_user") {
			return auditRows([]driver.Value{int64(42), "renamed-admin", "hash", "Display", nil, nil, int64(1), int64(0), "", "", int64(1), int64(0), ""}), nil
		}
		if strings.Contains(q, "from bp_role") {
			return auditRows([]driver.Value{"admin"}), nil
		}
		return nil, fmt.Errorf("unexpected query %s", q)
	}
	cfg := config.AuthConfig{Secret: strings.Repeat("a", 64), Issuer: "logout-test", AccessTokenTTL: time.Hour, RefreshTokenTTL: time.Hour}
	j, err := auth.NewJWTService(cfg)
	if err != nil {
		t.Fatal(err)
	}
	token, _, _, err := j.CreateRefreshToken(auth.User{ID: 42, Username: "old-name", TokenVersion: 1}, auth.DeviceFingerprint("203.0.113.1", "agent"), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", MaxRetries: -1})
	rdb.AddHook(&auditRedisHook{})
	defer rdb.Close()
	svc := auth.NewService(cfg, auth.NewRepository(repo.db), nil, j, nil, rdb, nil)
	svc.SetAuditRecorder(NewAuthAuditAdapter(NewService(repo, nil)))
	if err := svc.Logout(context.Background(), token, "203.0.113.1", "agent"); err != nil {
		t.Fatal(err)
	}
	if state.writes != 1 || state.args[1].Value != "renamed-admin" || state.args[20].Value != "admin" {
		t.Fatalf("logout identity/snapshot wrong: %v", state.args)
	}
}
