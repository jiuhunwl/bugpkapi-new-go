package gateway

import (
	"context"
	"database/sql/driver"
	"fmt"
	"strings"
	"testing"
	"time"

	"bugpk-api/internal/pkg/testdb"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

type stubTx struct{}

func (stubTx) Commit() error   { return nil }
func (stubTx) Rollback() error { return nil }

type stubResult struct {
	id, affected int64
}

func (r stubResult) LastInsertId() (int64, error) { return r.id, nil }
func (r stubResult) RowsAffected() (int64, error) { return r.affected, nil }

func newZeroPriceQuotaService(t *testing.T, query func(context.Context, string, []driver.NamedValue) (driver.Rows, error), exec func(context.Context, string, []driver.NamedValue) (driver.Result, error)) *Service {
	t.Helper()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	db := testdb.OpenStub(t, testdb.SQLStub{
		Begin: func(context.Context, driver.TxOptions) (driver.Tx, error) { return stubTx{}, nil },
		Query: query,
		Exec:  exec,
	})
	return &Service{repo: NewRepository(db), rdb: client, log: zap.NewNop(), now: time.Now}
}

// 有可用套餐时先扣套餐，不再消耗用户组免费额度；套餐 QPS 从第一次调用生效。
func TestPackageConsumedBeforeFreeQuota(t *testing.T) {
	api := InterfaceConfig{ID: 9, APICode: "free", Price: "0.0000", BillingDisabled: true}
	key := KeyAuth{
		ID:                     7,
		UserID:                 51,
		RoleKey:                "user",
		QuotaEnabled:           1,
		QuotaMinuteLimit:       1,
		QuotaCountMode:         quotaCountModeAttempt,
		HasUsableGlobalPackage: true,
	}
	service := newZeroPriceQuotaService(t, func(_ context.Context, q string, _ []driver.NamedValue) (driver.Rows, error) {
		switch {
		case strings.Contains(q, "primary_role.qps_limit"):
			return testdb.Rows([]driver.Value{int64(3)}), nil
		case strings.Contains(q, "select case"):
			return testdb.Rows([]driver.Value{""}), nil
		case strings.Contains(q, "select id,qps_limit from bp_user_package_global"):
			return testdb.Rows([]driver.Value{int64(101), int64(50)}), nil
		default:
			return nil, fmt.Errorf("unexpected query: %s", q)
		}
	}, func(_ context.Context, q string, _ []driver.NamedValue) (driver.Result, error) {
		switch {
		case strings.Contains(q, "day_used=0"):
			return stubResult{affected: 1}, nil
		case strings.Contains(q, "day_used=day_used+1"):
			return stubResult{affected: 1}, nil
		case strings.Contains(q, "insert into bp_billing_record"):
			return stubResult{id: 9001, affected: 1}, nil
		default:
			return nil, fmt.Errorf("unexpected exec: %s", q)
		}
	})

	ctx := context.Background()
	reservation, err := service.reserveRequestQuota(ctx, key, api)
	if err != nil {
		t.Fatalf("package request should pass: %v", err)
	}
	if reservation.Billing.ChargeType != "GLOBAL_PACKAGE" || reservation.Billing.BillingRecordID != 9001 {
		t.Fatalf("unexpected package reservation: %+v", reservation.Billing)
	}
	if reservation.Billing.QPSLimit != 50 {
		t.Fatalf("package QPS must override role QPS, got %d", reservation.Billing.QPSLimit)
	}
	keys, err := service.rdb.Keys(ctx, "*").Result()
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 0 {
		t.Fatalf("package must be consumed before free quota, found counters: %v", keys)
	}
}

// 没有任何可用套餐时保持用户组免费额度优先；免费额度用尽后 429。
func TestNoPackageUsesFreeQuotaThenRejects(t *testing.T) {
	api := InterfaceConfig{ID: 10, APICode: "free", Price: "0.0000", BillingDisabled: true}
	key := KeyAuth{
		ID:               8,
		UserID:           52,
		RoleKey:          "user",
		QuotaEnabled:     1,
		QuotaMinuteLimit: 1,
		QuotaCountMode:   quotaCountModeAttempt,
	}
	service := newZeroPriceQuotaService(t, func(_ context.Context, q string, _ []driver.NamedValue) (driver.Rows, error) {
		return nil, fmt.Errorf("unexpected query without package: %s", q)
	}, func(_ context.Context, q string, _ []driver.NamedValue) (driver.Result, error) {
		return nil, fmt.Errorf("unexpected exec without package: %s", q)
	})

	ctx := context.Background()
	if _, err := service.reserveRequestQuota(ctx, key, api); err != nil {
		t.Fatalf("first request should use free quota: %v", err)
	}
	if _, err := service.reserveRequestQuota(ctx, key, api); err == nil {
		t.Fatal("second request must be rejected when no package quota remains")
	} else if !strings.Contains(err.Error(), "免费额度") {
		t.Fatalf("unexpected rejection: %v", err)
	}
}
