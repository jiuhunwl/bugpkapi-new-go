package interfaceapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"testing"
	"time"

	"bugpk-api/internal/domain/rbac"
	apperrors "bugpk-api/internal/pkg/errors"
	"bugpk-api/internal/pkg/testdb"

	_ "github.com/go-sql-driver/mysql"
)

func TestSaveGlobalDeliveryPolicyTimeoutContract(t *testing.T) {
	dsn := os.Getenv("BUGPK_MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("BUGPK_MYSQL_TEST_DSN is not configured")
	}
	if err := testdb.ValidateIsolatedMySQLDSN(dsn); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	repo := NewRepository(db)
	original, err := repo.GlobalDeliveryPolicy(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := repo.SaveGlobalDeliveryPolicy(context.Background(), original, 0); err != nil {
			t.Error(err)
		}
	})
	service := &Service{repo: repo, authz: rbac.NewService(nil, nil)}
	principal := Principal{Roles: []string{"admin"}}
	for _, tc := range []struct {
		name, payload string
		invalid       bool
		want          int
	}{
		{"explicit zero", "{\"requestTimeoutMs\":0}", true, 60000},
		{"zero with inheritance flag", "{\"requestTimeoutMs\":0,\"requestTimeoutOverride\":0}", true, 60000},
		{"negative", "{\"requestTimeoutMs\":-1}", true, 60000},
		{"below minimum", "{\"requestTimeoutMs\":99}", true, 60000},
		{"above maximum", "{\"requestTimeoutMs\":120001}", true, 60000},
		{"legacy omitted timeout preserves global", "{\"cacheEnabled\":0}", false, 60000},
		{"minimum", "{\"requestTimeoutMs\":100}", false, 100},
		{"maximum", "{\"requestTimeoutMs\":120000}", false, 120000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			seed := original
			seed.RequestTimeoutMs = 60000
			if err := repo.SaveGlobalDeliveryPolicy(ctx, seed, 0); err != nil {
				t.Fatal(err)
			}
			before, err := repo.GlobalDeliveryPolicy(ctx)
			if err != nil {
				t.Fatal(err)
			}
			var req SaveDeliveryPolicyRequest
			if err := json.Unmarshal([]byte(tc.payload), &req); err != nil {
				t.Fatal(err)
			}
			notifications := 0
			service.SetInterfaceChanged(func(string, string) { notifications++ })
			err = service.SaveGlobalDeliveryPolicy(ctx, principal, req)
			if tc.invalid {
				business, ok := apperrors.AsBusinessError(err)
				if !ok || business.StatusCode != 400 {
					t.Fatalf("invalid global timeout must return HTTP 400, got %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			after, err := repo.GlobalDeliveryPolicy(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if after.RequestTimeoutMs != tc.want {
				t.Fatalf("global timeout=%d, want %d", after.RequestTimeoutMs, tc.want)
			}
			if tc.invalid && (after.PolicyVersion != before.PolicyVersion || notifications != 0) {
				t.Fatal("rejected timeout mutated global policy or invalidated interfaces")
			}
			if !tc.invalid && (after.PolicyVersion <= before.PolicyVersion || notifications != 1) {
				t.Fatal("accepted global policy did not invalidate interface configuration")
			}
		})
	}
}
