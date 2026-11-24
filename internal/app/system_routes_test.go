package app

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"bugpk-api/internal/config"
	"bugpk-api/internal/domain/auth"
	"bugpk-api/internal/domain/observability"
	"bugpk-api/internal/domain/rbac"
	"bugpk-api/internal/middleware"
	"bugpk-api/internal/pkg/response"
	"bugpk-api/internal/pkg/testdb"
	hertzapp "github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

type systemAuditState struct {
	principalQueries, roleQueries, handled int
	version, status                        int64
	role                                   string
	writes                                 [][]driver.NamedValue
	lookupErr                              error
}

func systemAuditFixture(t *testing.T) (*observability.Service, *rbac.Service, string, *systemAuditState) {
	t.Helper()
	state := &systemAuditState{version: 1, status: 1, role: "admin"}
	db := testdb.OpenStub(t, testdb.SQLStub{
		Query: func(_ context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
			switch {
			case strings.Contains(q, "from bp_user"):
				state.principalQueries++
				if state.lookupErr != nil {
					return nil, state.lookupErr
				}
				return testdb.Rows([]driver.Value{int64(42), "canonical", state.status, int64(0), "", "", state.version}), nil
			case strings.Contains(q, "from bp_role"):
				state.roleQueries++
				return testdb.Rows([]driver.Value{state.role}), nil
			default:
				return nil, fmt.Errorf("unexpected query %s", q)
			}
		},
		Exec: func(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
			if !strings.Contains(q, "insert into bp_operation_log") {
				return nil, errors.New("unexpected write")
			}
			if _, ok := ctx.Deadline(); !ok || ctx.Err() != nil {
				t.Errorf("audit write must have a usable bounded context")
			}
			state.writes = append(state.writes, append([]driver.NamedValue(nil), args...))
			return driver.RowsAffected(1), nil
		},
	})
	jwt, err := auth.NewJWTService(config.AuthConfig{Secret: strings.Repeat("s", 64), Issuer: "system-audit-test", AccessTokenTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	token, err := jwt.CreateAccessToken(auth.User{ID: 42, Username: "canonical", TokenVersion: 1}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	authorizer := rbac.NewService(rbac.NewRepository(db), jwt)
	return observability.NewService(observability.NewRepository(db), authorizer), authorizer, token, state
}

func TestSystemAuditAndBusinessShareOnlyCurrentRequestAuthentication(t *testing.T) {
	audit, authorizer, token, state := systemAuditFixture(t)
	h := server.New()
	group := newSystemRouteGroup(h, "/system-api", audit.OperationAuditMiddleware(), zap.NewNop())
	group.PUT("/interface/:id", func(ctx context.Context, c *hertzapp.RequestContext) {
		principal, err := authorizer.Authenticate(ctx, token)
		if err != nil {
			response.FromError(c, err)
			return
		}
		if principal.Roles[0] != "admin" {
			c.SetStatusCode(403)
			return
		}
		state.handled++
		c.SetStatusCode(200)
	})
	perform := func() int {
		return ut.PerformRequest(h.Engine, "PUT", "/system-api/interface/7", nil, ut.Header{Key: "Authorization", Value: "Bearer " + token}).Code
	}
	if code := perform(); code != 200 {
		t.Fatalf("first request: %d", code)
	}
	if state.principalQueries != 1 || state.roleQueries != 1 {
		t.Fatalf("one request performed duplicate identity lookups: principal=%d roles=%d", state.principalQueries, state.roleQueries)
	}
	if len(state.writes) != 1 {
		t.Fatalf("audit count=%d", len(state.writes))
	}
	state.role = "user"
	if code := perform(); code != 403 {
		t.Fatalf("new request reused old admin role: %d", code)
	}
	if state.handled != 1 {
		t.Fatal("demoted user reached the protected operation")
	}
	state.version = 2
	if code := perform(); code != 401 {
		t.Fatalf("new request reused revoked token: %d", code)
	}
	state.version = 1
	state.status = 0
	if code := perform(); code != 403 {
		t.Fatalf("new request reused disabled account: %d", code)
	}
}

func TestSystemAuditSurvivesBusinessPanicAfterUnifiedRecovery(t *testing.T) {
	for _, prefix := range []string{"/system-api", "/custom-system"} {
		t.Run(prefix, func(t *testing.T) {
			audit, authorizer, token, state := systemAuditFixture(t)
			core, logs := observer.New(zap.ErrorLevel)
			logger := zap.New(core)
			h := server.New()
			h.Use(middleware.Recovery(logger))
			group := newSystemRouteGroup(h, prefix, audit.OperationAuditMiddleware(prefix), logger)
			group.PUT("/interface/:id", func(ctx context.Context, c *hertzapp.RequestContext) {
				if _, err := authorizer.Authenticate(ctx, token); err != nil {
					response.FromError(c, err)
					return
				}
				panic("password=panic-private-value")
			})
			result := ut.PerformRequest(h.Engine, "PUT", prefix+"/interface/7?token=query-private-value", nil, ut.Header{Key: "Authorization", Value: "Bearer " + token})
			if result.Code != 500 || !strings.Contains(result.Body.String(), "error_id") {
				t.Fatalf("panic response is not unified 500: %d %s", result.Code, result.Body.String())
			}
			if len(state.writes) != 1 {
				t.Fatalf("panic lost or duplicated audit: got %d, want 1", len(state.writes))
			}
			row := state.writes[0]
			if row[0].Value != int64(42) || row[8].Value != "7" || row[9].Value != int64(0) || row[10].Value != "HTTP 500" || row[11].Value != int64(500) {
				t.Fatalf("panic audit has wrong attribution/status: %v", row)
			}
			if logs.FilterMessage("panic recovered").Len() != 1 {
				t.Fatalf("panic recovery log count=%d", logs.Len())
			}
			text := fmt.Sprint(row) + result.Body.String() + fmt.Sprint(logs.All())
			for _, secret := range []string{token, "panic-private-value", "query-private-value"} {
				if strings.Contains(text, secret) {
					t.Fatal("panic leaked sensitive input")
				}
			}
		})
	}
}

func TestSystemAuditRetainsAuthEventWhenLoginHandlerPanics(t *testing.T) {
	audit, _, _, state := systemAuditFixture(t)
	core, logs := observer.New(zap.ErrorLevel)
	logger := zap.New(core)
	h := server.New()
	h.Use(middleware.Recovery(logger))
	group := newSystemRouteGroup(h, "/system-api", audit.OperationAuditMiddleware(), logger)
	group.POST("/auth/login", func(ctx context.Context, _ *hertzapp.RequestContext) {
		observability.NewAuthAuditAdapter(audit).RecordAuth(ctx, auth.AuditEntry{UserID: 42, Username: "canonical", Roles: []string{"admin"}, Action: "login", Status: true})
		panic("password=login-private-value")
	})
	result := ut.PerformRequest(h.Engine, "POST", "/system-api/auth/login", nil)
	if result.Code != 500 || len(state.writes) != 1 {
		t.Fatalf("login panic result=%d audit count=%d", result.Code, len(state.writes))
	}
	if state.writes[0][9].Value != int64(0) || state.writes[0][11].Value != int64(500) {
		t.Fatalf("panic login falsely recorded as successful: %v", state.writes[0])
	}
	if logs.FilterMessage("panic recovered").Len() != 1 {
		t.Fatal("login panic should be logged once")
	}
}

func TestSystemAuditRecoversIdentityAfterTransientPreAuthenticationFailure(t *testing.T) {
	audit, authorizer, token, state := systemAuditFixture(t)
	state.lookupErr = errors.New("transient identity store failure")
	h := server.New()
	group := newSystemRouteGroup(h, "/system-api", audit.OperationAuditMiddleware(), zap.NewNop())
	group.PUT("/interface/:id", func(ctx context.Context, c *hertzapp.RequestContext) {
		state.lookupErr = nil
		if _, err := authorizer.Authenticate(ctx, token); err != nil {
			response.FromError(c, err)
			return
		}
		// A following mutation could remove this account: audit must use the proven
		// request identity, not query again after the operation.
		state.status = 0
		c.SetStatusCode(200)
	})
	result := ut.PerformRequest(h.Engine, "PUT", "/system-api/interface/7", nil, ut.Header{Key: "Authorization", Value: "Bearer " + token})
	if result.Code != 200 || len(state.writes) != 1 {
		t.Fatalf("successful authenticated operation lost its audit: status=%d writes=%d", result.Code, len(state.writes))
	}
	if state.principalQueries != 2 || state.writes[0][0].Value != int64(42) {
		t.Fatalf("audit reauthenticated after mutation or lost its principal: queries=%d", state.principalQueries)
	}
}

func TestSystemAuditPreservesStatusAndSkipsPublicGateway(t *testing.T) {
	for _, status := range []int{200, 201, 403, 500, 302} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			audit, authorizer, token, state := systemAuditFixture(t)
			h := server.New()
			group := newSystemRouteGroup(h, "/system-api", audit.OperationAuditMiddleware(), zap.NewNop())
			group.PUT("/interface/:id", func(ctx context.Context, c *hertzapp.RequestContext) {
				if _, err := authorizer.Authenticate(ctx, token); err != nil {
					response.FromError(c, err)
					return
				}
				c.SetStatusCode(status)
			})
			result := ut.PerformRequest(h.Engine, "PUT", "/system-api/interface/7", nil, ut.Header{Key: "Authorization", Value: "Bearer " + token})
			if result.Code != status {
				t.Fatalf("response status changed: %d", result.Code)
			}
			want := 1
			if status == 302 {
				want = 0
			}
			if len(state.writes) != want {
				t.Fatalf("audit count=%d, want %d", len(state.writes), want)
			}
			if want == 1 {
				success := int64(0)
				if status < 300 {
					success = 1
				}
				if state.writes[0][9].Value != success || state.writes[0][11].Value != int64(status) {
					t.Fatal("audit status diverged from response")
				}
			}
			h.POST("/api/demo", func(_ context.Context, c *hertzapp.RequestContext) { c.SetStatusCode(200) })
			ut.PerformRequest(h.Engine, "POST", "/api/demo", nil, ut.Header{Key: "Authorization", Value: "Bearer " + token})
			if len(state.writes) != want || state.principalQueries != 1 {
				t.Fatal("open gateway inherited system audit/authentication")
			}
		})
	}
}

func TestSystemAuditStillWritesAfterCancelledBusinessPanic(t *testing.T) {
	audit, authorizer, token, state := systemAuditFixture(t)
	core, logs := observer.New(zap.ErrorLevel)
	logger := zap.New(core)
	h := server.New()
	var cancel context.CancelFunc
	h.Use(middleware.Recovery(logger), func(ctx context.Context, c *hertzapp.RequestContext) {
		ctx, cancel = context.WithCancel(ctx)
		defer cancel()
		c.Next(ctx)
	})
	group := newSystemRouteGroup(h, "/system-api", audit.OperationAuditMiddleware(), logger)
	group.PUT("/interface/:id", func(ctx context.Context, c *hertzapp.RequestContext) {
		if _, err := authorizer.Authenticate(ctx, token); err != nil {
			response.FromError(c, err)
			return
		}
		cancel()
		panic("password=cancelled-private-value")
	})
	result := ut.PerformRequest(h.Engine, "PUT", "/system-api/interface/7", nil, ut.Header{Key: "Authorization", Value: "Bearer " + token})
	if result.Code != 500 || len(state.writes) != 1 {
		t.Fatalf("cancellation lost failed audit: status=%d writes=%d", result.Code, len(state.writes))
	}
	if state.writes[0][10].Value != "HTTP 500" || logs.FilterMessage("panic recovered").Len() != 1 {
		t.Fatal("cancelled panic was not recovered once")
	}
}
