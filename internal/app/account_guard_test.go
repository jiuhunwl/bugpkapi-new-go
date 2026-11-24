package app

import (
	"context"
	"database/sql/driver"
	"fmt"
	"strings"
	"testing"
	"time"

	"bugpk-api/internal/config"
	"bugpk-api/internal/domain/auth"
	"bugpk-api/internal/domain/rbac"
	"bugpk-api/internal/pkg/response"
	"bugpk-api/internal/pkg/testdb"

	hertzapp "github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"go.uber.org/zap"
)

type accountGuardState struct {
	status    int64
	permanent int64
	until     string
	reason    string
	version   int64

	userQueries int
	roleQueries int
	handled     int
}

// accountGuardFixture builds the smallest possible /system-api tree: the real
// group composition plus one probe handler. The probe records whether the
// request reached business code, which is the only thing a ban has to prevent.
func accountGuardFixture(t *testing.T) (*rbac.Service, string, *accountGuardState) {
	t.Helper()
	state := &accountGuardState{status: 1, version: 1}
	db := testdb.OpenStub(t, testdb.SQLStub{
		Query: func(_ context.Context, q string, _ []driver.NamedValue) (driver.Rows, error) {
			switch {
			case strings.Contains(q, "from bp_user"):
				state.userQueries++
				return testdb.Rows([]driver.Value{
					int64(42), "canonical", state.status,
					state.permanent, state.until, state.reason, state.version,
				}), nil
			case strings.Contains(q, "from bp_role"):
				state.roleQueries++
				return testdb.Rows([]driver.Value{"user"}), nil
			default:
				return nil, fmt.Errorf("unexpected query %s", q)
			}
		},
	})
	jwt, err := auth.NewJWTService(config.AuthConfig{
		Secret:         strings.Repeat("g", 64),
		Issuer:         "account-guard-test",
		AccessTokenTTL: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	token, err := jwt.CreateAccessToken(auth.User{ID: 42, Username: "canonical", TokenVersion: 1}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return rbac.NewService(rbac.NewRepository(db), jwt), token, state
}

// guardProbe registers a handler that re-authenticates, mirroring what every
// production handler does. Re-authentication must reuse the guard's result.
func guardProbe(t *testing.T, authorizer *rbac.Service, token string, state *accountGuardState) *server.Hertz {
	t.Helper()
	h := server.New()
	h.Use(newAccountStatusGuard(authorizer))
	group := newSystemRouteGroup(h, "/system-api", func(ctx context.Context, c *hertzapp.RequestContext) { c.Next(ctx) }, zap.NewNop())
	group.GET("/probe", func(ctx context.Context, c *hertzapp.RequestContext) {
		if _, err := authorizer.Authenticate(ctx, token); err != nil {
			response.FromError(c, err)
			return
		}
		state.handled++
		c.SetStatusCode(consts.StatusOK)
	})
	return h
}

func TestAccountGuardRejectsPermanentBanBeforeAnyHandlerRuns(t *testing.T) {
	authorizer, token, state := accountGuardFixture(t)
	state.status, state.permanent, state.reason = 0, 1, "批量注册（临时邮箱）"

	result := ut.PerformRequest(guardProbe(t, authorizer, token, state).Engine,
		"GET", "/system-api/probe", nil,
		ut.Header{Key: "Authorization", Value: "Bearer " + token})

	if result.Code != consts.StatusForbidden {
		t.Fatalf("banned account reached the tree: status=%d body=%s", result.Code, result.Body.String())
	}
	if state.handled != 0 {
		t.Fatal("business handler ran for a banned account")
	}
	if !strings.Contains(result.Body.String(), "ACCOUNT_BANNED") {
		t.Fatalf("rejection lost its ban marker: %s", result.Body.String())
	}
	if got := string(result.Header().Peek("X-Bugpk-Ban-Type")); got != "PERMANENT" {
		t.Fatalf("ban type header = %q", got)
	}
	if got := string(result.Header().Peek("X-Bugpk-Account-Status")); got != "BANNED" {
		t.Fatalf("account status header = %q", got)
	}
}

func TestAccountGuardRejectsTemporaryBanAndReportsExpiry(t *testing.T) {
	authorizer, token, state := accountGuardFixture(t)
	state.status, state.permanent, state.until, state.reason = 0, 0, "2026-11-05 09:30:00", "多次超频调用"

	result := ut.PerformRequest(guardProbe(t, authorizer, token, state).Engine,
		"GET", "/system-api/probe", nil,
		ut.Header{Key: "Authorization", Value: "Bearer " + token})

	if result.Code != consts.StatusForbidden || state.handled != 0 {
		t.Fatalf("temporary ban leaked through: status=%d handled=%d", result.Code, state.handled)
	}
	if got := string(result.Header().Peek("X-Bugpk-Ban-Type")); got != "TEMPORARY" {
		t.Fatalf("ban type header = %q", got)
	}
	if got := string(result.Header().Peek("X-Bugpk-Ban-Until")); got != "2026-11-05 09:30:00" {
		t.Fatalf("ban until header = %q", got)
	}
}

func TestAccountGuardLeavesAnonymousTrafficAlone(t *testing.T) {
	authorizer, _, state := accountGuardFixture(t)
	state.status, state.permanent = 0, 1

	h := server.New()
	h.Use(newAccountStatusGuard(authorizer))
	group := newSystemRouteGroup(h, "/system-api", func(ctx context.Context, c *hertzapp.RequestContext) { c.Next(ctx) }, zap.NewNop())
	group.GET("/probe", func(ctx context.Context, c *hertzapp.RequestContext) {
		state.handled++
		c.SetStatusCode(consts.StatusOK)
	})

	result := ut.PerformRequest(h.Engine, "GET", "/system-api/probe", nil)

	if result.Code != consts.StatusOK || state.handled != 1 {
		t.Fatalf("anonymous public route blocked: status=%d handled=%d", result.Code, state.handled)
	}
	if state.userQueries != 0 {
		t.Fatalf("anonymous request hit the user store %d times", state.userQueries)
	}
}

// An expired access token must keep producing the handler's 401 so the browser
// can refresh. If the guard swallowed it, every user would be logged out the
// moment their access token aged out.
func TestAccountGuardDoesNotTerminateExpiredCredentials(t *testing.T) {
	authorizer, _, state := accountGuardFixture(t)
	jwt, err := auth.NewJWTService(config.AuthConfig{
		Secret:         strings.Repeat("x", 64),
		Issuer:         "account-guard-test",
		AccessTokenTTL: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Same claims, different signing key: the guard cannot verify it and must
	// step aside instead of answering with a terminal status of its own.
	foreign, err := jwt.CreateAccessToken(auth.User{ID: 42, Username: "canonical", TokenVersion: 1}, time.Now())
	if err != nil {
		t.Fatal(err)
	}

	result := ut.PerformRequest(guardProbe(t, authorizer, foreign, state).Engine,
		"GET", "/system-api/probe", nil,
		ut.Header{Key: "Authorization", Value: "Bearer " + foreign})

	if result.Code != consts.StatusUnauthorized {
		t.Fatalf("handler semantics changed for invalid credentials: %d", result.Code)
	}
	if state.handled != 0 || state.userQueries != 0 {
		t.Fatalf("guard looked up an unverifiable credential: queries=%d", state.userQueries)
	}
}

func TestAccountGuardDoesNotTerminateTokenVersionMismatch(t *testing.T) {
	authorizer, token, state := accountGuardFixture(t)
	// status stays 1: this is a credential revoked by a password change, not a
	// ban. It must reach the handler as 401 so the client refreshes or re-logs.
	state.version = 2

	result := ut.PerformRequest(guardProbe(t, authorizer, token, state).Engine,
		"GET", "/system-api/probe", nil,
		ut.Header{Key: "Authorization", Value: "Bearer " + token})

	if result.Code != consts.StatusUnauthorized || state.handled != 0 {
		t.Fatalf("stale token changed semantics: status=%d handled=%d", result.Code, state.handled)
	}
	if strings.Contains(result.Body.String(), "ACCOUNT_BANNED") {
		t.Fatal("a revoked credential was reported as a ban")
	}
}

func TestAccountGuardSharesAuthenticationWithHandler(t *testing.T) {
	authorizer, token, state := accountGuardFixture(t)

	result := ut.PerformRequest(guardProbe(t, authorizer, token, state).Engine,
		"GET", "/system-api/probe", nil,
		ut.Header{Key: "Authorization", Value: "Bearer " + token})

	if result.Code != consts.StatusOK || state.handled != 1 {
		t.Fatalf("healthy account blocked: status=%d handled=%d", result.Code, state.handled)
	}
	if state.userQueries != 1 || state.roleQueries != 1 {
		t.Fatalf("guard duplicated identity lookups: user=%d roles=%d", state.userQueries, state.roleQueries)
	}
}

func TestAccountGuardCoversNestedRouteGroups(t *testing.T) {
	authorizer, token, state := accountGuardFixture(t)
	state.status, state.permanent, state.reason = 0, 1, "批量注册"

	h := server.New()
	h.Use(newAccountStatusGuard(authorizer))
	group := newSystemRouteGroup(h, "/system-api", func(ctx context.Context, c *hertzapp.RequestContext) { c.Next(ctx) }, zap.NewNop())
	// Production registers every business handler on a nested group, so the
	// guard has to be inherited rather than applied per leaf route.
	nested := group.Group("/user")
	nested.GET("/info", func(_ context.Context, c *hertzapp.RequestContext) {
		state.handled++
		c.SetStatusCode(consts.StatusOK)
	})

	result := ut.PerformRequest(h.Engine, "GET", "/system-api/user/info", nil,
		ut.Header{Key: "Authorization", Value: "Bearer " + token})

	if result.Code != consts.StatusForbidden || state.handled != 0 {
		t.Fatalf("nested route bypassed the guard: status=%d handled=%d", result.Code, state.handled)
	}
}

func TestAccountGuardNilAuthorizerIsInert(t *testing.T) {
	state := &accountGuardState{status: 0, permanent: 1, version: 1}
	h := server.New()
	h.Use(newAccountStatusGuard(nil))
	group := newSystemRouteGroup(h, "/system-api", func(ctx context.Context, c *hertzapp.RequestContext) { c.Next(ctx) }, zap.NewNop())
	group.GET("/probe", func(_ context.Context, c *hertzapp.RequestContext) {
		state.handled++
		c.SetStatusCode(consts.StatusOK)
	})

	result := ut.PerformRequest(h.Engine, "GET", "/system-api/probe", nil,
		ut.Header{Key: "Authorization", Value: "Bearer anything"})

	if result.Code != consts.StatusOK || state.handled != 1 {
		t.Fatalf("inert guard changed behaviour: status=%d handled=%d", result.Code, state.handled)
	}
}

func TestBearerTokenFromRequest(t *testing.T) {
	cases := map[string]string{
		"Bearer abc":   "abc",
		"bearer abc":   "abc",
		"BEARER abc":   "abc",
		"Bearer  abc ": "abc",
		"Basic abc":    "",
		"Bearer":       "",
		"":             "",
	}
	for header, want := range cases {
		c := hertzapp.NewContext(0)
		if header != "" {
			c.Request.Header.Set("Authorization", header)
		}
		if got := bearerTokenFromRequest(c); got != want {
			t.Fatalf("header %q -> %q, want %q", header, got, want)
		}
	}
}
