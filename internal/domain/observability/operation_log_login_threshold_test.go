package observability

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"testing"

	"bugpk-api/internal/config"
	"bugpk-api/internal/domain/auth"
	apperrors "bugpk-api/internal/pkg/errors"
	"bugpk-api/internal/pkg/response"
	hertzapp "github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"github.com/redis/go-redis/v9"
	"golang.org/x/crypto/bcrypt"
)

type auditThresholdHook struct {
	auditRedisHook
	lockErr    error
	lockWrites int
}

func (h *auditThresholdHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		if c, ok := cmd.(*redis.Cmd); ok {
			h.increments++
			c.SetVal(int64(5))
			return nil
		}
		if c, ok := cmd.(*redis.StatusCmd); ok && strings.Contains(fmt.Sprint(cmd.Args()), "auth:login:lock:account:") {
			h.lockWrites++
			if h.lockErr != nil {
				c.SetErr(h.lockErr)
				return h.lockErr
			}
			h.locked = true
			c.SetVal("OK")
			return nil
		}
		return h.auditRedisHook.ProcessHook(next)(ctx, cmd)
	}
}

func TestOperationLoginThresholdAndLimiterErrorRemainAttributed(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("correct-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	for _, known := range []bool{true, false} {
		for _, lockFailure := range []bool{false, true} {
			for _, http := range []bool{false, true} {
				t.Run(fmt.Sprintf("known=%t/failure=%t/http=%t", known, lockFailure, http), func(t *testing.T) {
					repo, state := auditTestRepo(t)
					userLookups := 0
					state.query = func(q string, args []driver.NamedValue) (driver.Rows, error) {
						switch {
						case strings.Contains(q, "bp_captcha_global_config"):
							return auditRows([]driver.Value{int64(0)}), nil
						case strings.Contains(q, "from bp_user"):
							userLookups++
							if !known {
								return &operationRows{width: 13}, nil
							}
							return auditRows([]driver.Value{int64(42), "canonical", string(hash), "Canonical", "admin@example.com", nil, int64(1), int64(0), "", "", int64(1), int64(0), ""}), nil
						case strings.Contains(q, "from bp_role"):
							return auditRows([]driver.Value{"admin"}), nil
						default:
							return nil, fmt.Errorf("unexpected query %s", q)
						}
					}
					sentinel := errors.New("secret-limiter-error")
					hook := &auditThresholdHook{}
					if lockFailure {
						hook.lockErr = sentinel
					}
					rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", MaxRetries: -1})
					rdb.AddHook(hook)
					defer rdb.Close()
					cfg := config.AuthConfig{Secret: strings.Repeat("a", 64)}
					authRepo := auth.NewRepository(repo.db)
					svc := auth.NewService(cfg, authRepo, auth.NewCaptchaService(config.CaptchaConfig{}, authRepo, rdb), nil, auth.NewRateLimiter(rdb, cfg.Secret), rdb, nil)
					auditSvc := NewService(repo, nil)
					svc.SetAuditRecorder(NewAuthAuditAdapter(auditSvc))
					var loginErr error
					login := func(ctx context.Context) {
						_, loginErr = svc.Login(ctx, auth.LoginRequest{Username: "admin@example.com", Password: "wrong-password"}, "binding", "203.0.113.1", "agent")
					}
					if http {
						h := server.New()
						g := h.Group("/system-api")
						g.Use(auditSvc.OperationAuditMiddleware())
						g.POST("/auth/login", func(ctx context.Context, c *hertzapp.RequestContext) { login(ctx); response.FromError(c, loginErr) })
						ut.PerformRequest(h.Engine, "POST", "/system-api/auth/login", nil)
					} else {
						login(context.Background())
					}
					wantStatus := 403
					if lockFailure {
						wantStatus = 503
					}
					business, ok := apperrors.AsBusinessError(loginErr)
					if !ok || business.StatusCode != wantStatus {
						t.Fatalf("limiter outcome changed: %v", loginErr)
					}
					if lockFailure && !errors.Is(loginErr, sentinel) {
						t.Fatalf("original limiter error lost: %v", loginErr)
					}
					if hook.increments != 3 || hook.lockWrites != 1 || userLookups != 1 {
						t.Fatalf("limiter/lookup behavior changed: %+v lookups=%d", hook, userLookups)
					}
					wantWrites := 0
					if known {
						wantWrites = 1
					}
					if state.writes != wantWrites {
						t.Fatalf("threshold credential event lost/noise added: writes=%d want=%d", state.writes, wantWrites)
					}
					if known && (state.args[0].Value != int64(42) || state.args[1].Value != "canonical" || state.args[4].Value != "登录失败：账号或密码错误" || state.args[11].Value != int64(wantStatus)) {
						t.Fatalf("identity/reason/outcome lost: %v", state.args)
					}
					if strings.Contains(fmt.Sprint(state.args), "secret-limiter-error") {
						t.Fatal("raw limiter failure leaked into audit")
					}
					// Lock succeeded: the next attempt is blocked BEFORE credentials are checked.
					// Current limiter returns 403 on the fifth wrong password, then 429 here.
					if !lockFailure {
						login(context.Background())
						business, ok = apperrors.AsBusinessError(loginErr)
						if !ok || business.StatusCode != 429 || state.writes != wantWrites || userLookups != 1 || hook.increments != 3 {
							t.Fatalf("pre-login block changed or was audited: %v writes=%d lookups=%d", loginErr, state.writes, userLookups)
						}
					}
				})
			}
		}
	}
}
