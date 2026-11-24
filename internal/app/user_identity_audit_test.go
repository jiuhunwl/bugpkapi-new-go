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
	userdomain "bugpk-api/internal/domain/user"
	"bugpk-api/internal/pkg/response"
	"bugpk-api/internal/pkg/testdb"
	hertzapp "github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"go.uber.org/zap"
)

func TestUserDomainAuthenticationRemainsAttributableAfterAuditLookupFailure(t *testing.T) {
	for _, tc := range []struct {
		name            string
		preFailure      bool
		status, version int64
		http, writes    int
	}{
		{"normal", false, 1, 1, 200, 1}, {"recovered pre-authentication", true, 1, 1, 200, 1},
		{"disabled", true, 0, 1, 403, 0}, {"revoked", true, 1, 2, 401, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rbacReads, userReads := 0, 0
			var writes [][]driver.NamedValue
			db := testdb.OpenStub(t, testdb.SQLStub{
				Query: func(_ context.Context, q string, _ []driver.NamedValue) (driver.Rows, error) {
					switch {
					case strings.Contains(q, "from bp_user_specified_response"):
						return testdb.Rows([]driver.Value{int64(0), int64(0)}), nil
					case strings.Contains(q, "password_hash"):
						userReads++
						return testdb.Rows([]driver.Value{int64(42), "full-user", "test-hash", int64(1), "Full User", "", "", "", "/console/workspace", "0.0000", int64(0), tc.status, int64(0), nil, "", tc.version, int64(2), nil, "2026-10-06 00:00:00"}), nil
					case strings.Contains(q, "from bp_user"):
						rbacReads++
						if tc.preFailure {
							return nil, errors.New("transient audit lookup failure")
						}
						return testdb.Rows([]driver.Value{int64(42), "full-user", tc.status, int64(0), "", "", tc.version}), nil
					case strings.Contains(q, "select r.id"):
						return testdb.Rows([]driver.Value{int64(2), "user", "User"}), nil
					case strings.Contains(q, "from bp_role"):
						return testdb.Rows([]driver.Value{"user"}), nil
					default:
						return nil, fmt.Errorf("unexpected query %s", q)
					}
				},
				Exec: func(_ context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
					if !strings.Contains(q, "insert into bp_operation_log") {
						return nil, errors.New("unexpected write")
					}
					writes = append(writes, append([]driver.NamedValue(nil), args...))
					return driver.RowsAffected(1), nil
				},
			})
			jwt, err := auth.NewJWTService(config.AuthConfig{Secret: strings.Repeat("u", 64), Issuer: "user-audit-test", AccessTokenTTL: time.Hour})
			if err != nil {
				t.Fatal(err)
			}
			token, err := jwt.CreateAccessToken(auth.User{ID: 42, Username: "full-user", TokenVersion: 1}, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			authorizer := rbac.NewService(rbac.NewRepository(db), jwt)
			audit := observability.NewService(observability.NewRepository(db), authorizer)
			users := userdomain.NewService(userdomain.NewRepository(db), jwt, nil, nil, nil)
			h := server.New()
			group := newSystemRouteGroup(h, "/system-api", audit.OperationAuditMiddleware(), zap.NewNop())
			group.PUT("/user/profile", func(ctx context.Context, c *hertzapp.RequestContext) {
				principal, _, err := users.Authenticate(ctx, token)
				if err != nil {
					response.FromError(c, err)
					return
				}
				if principal.ID != 42 {
					t.Error("wrong user")
				}
				c.SetStatusCode(200)
			})
			result := ut.PerformRequest(h.Engine, "PUT", "/system-api/user/profile", nil, ut.Header{Key: "Authorization", Value: "Bearer " + token})
			if result.Code != tc.http || len(writes) != tc.writes {
				t.Fatalf("user operation status=%d audit=%d, want %d/%d", result.Code, len(writes), tc.http, tc.writes)
			}
			if rbacReads != 1 || userReads != 1 {
				t.Fatalf("audit should not query identity after the user operation: RBAC=%d user=%d", rbacReads, userReads)
			}
			if len(writes) > 0 && (writes[0][0].Value != int64(42) || writes[0][20].Value != "user") {
				t.Fatal("user audit attribution was lost")
			}
		})
	}
}
