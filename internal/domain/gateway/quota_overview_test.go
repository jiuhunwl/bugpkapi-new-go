package gateway

import (
	"context"
	"database/sql/driver"
	"strings"
	"testing"
	"time"

	"bugpk-api/internal/pkg/businessclock"
	"bugpk-api/internal/pkg/testdb"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

// 概览必须把「用户组免费额度」和「付费额度」分开返回：免费窗口的用量来自
// Redis 计数，付费额度来自套餐/点数/余额，两边不合并。
func TestQuotaOverviewSeparatesFreeAndPaid(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	now := time.Date(2026, 11, 4, 13, 30, 0, 0, businessclock.Location())
	local := now.In(businessclock.Location())
	createdAt := now.Add(-2 * time.Hour)
	// 预置分钟窗口已用 3 次，其余窗口没有键。键名必须与闸门一致。
	server.Set("openapi:quota:request:v1:61:minute:"+local.Format("200601021504"), "3")

	db := testdb.OpenStub(t, testdb.SQLStub{
		Query: func(_ context.Context, q string, _ []driver.NamedValue) (driver.Rows, error) {
			if strings.Contains(q, "bp_auth_register_config") {
				return testdb.Rows([]driver.Value{
					int64(61), int64(2), "普通用户", "user", int64(1),
					int64(15), int64(600), int64(8000), int64(150000),
					"attempt", int64(100), createdAt, int64(0), "0.0000",
				}), nil
			}
			// PaidPackageUsages：没有生效套餐。
			return testdb.Rows(), nil
		},
	})
	service := &Service{repo: NewRepository(db), rdb: client, log: zap.NewNop(), now: func() time.Time { return now }}

	overview, err := service.QuotaOverview(context.Background(), 61)
	if err != nil {
		t.Fatalf("overview failed: %v", err)
	}
	if !overview.CounterAvailable {
		t.Fatal("redis counters are reachable, CounterAvailable must be true")
	}
	if overview.Role.Name != "普通用户" || !overview.Role.QuotaEnabled {
		t.Fatalf("unexpected role brief: %+v", overview.Role)
	}
	if len(overview.Free) != 4 {
		t.Fatalf("expected 4 free windows, got %d", len(overview.Free))
	}
	byWindow := map[string]QuotaWindowUsage{}
	for _, item := range overview.Free {
		byWindow[item.Window] = item
	}
	minute := byWindow[quotaWindowMinute]
	if minute.Limit != 15 || minute.Used != 3 || minute.Remaining != 12 || minute.Exhausted {
		t.Fatalf("unexpected minute window: %+v", minute)
	}
	day := byWindow[quotaWindowDay]
	if day.Limit != 8000 || day.Used != 0 || day.Remaining != 8000 {
		t.Fatalf("unexpected day window: %+v", day)
	}
	if overview.New == nil {
		t.Fatal("new-account window must be present while inside the first 24h")
	}
	if overview.New.Limit != 100 || overview.New.Window != quotaWindowNewAccount {
		t.Fatalf("unexpected new-account window: %+v", overview.New)
	}
	if overview.New.ResetAt != createdAt.Add(24*time.Hour).In(businessclock.Location()).Format("2006-01-02 15:04:05") {
		t.Fatalf("new-account reset time must be registration + 24h, got %q", overview.New.ResetAt)
	}
	if overview.Paid.Available {
		t.Fatal("no package, no points, zero balance: paid quota must be unavailable")
	}
}

// 用户组关闭总量配额时，免费窗口不应凭空出现在概览里（否则用户以为自己被限了）。
func TestQuotaOverviewOmitsFreeWindowsWhenRoleQuotaDisabled(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	now := time.Date(2026, 11, 4, 13, 30, 0, 0, businessclock.Location())
	createdAt := now.Add(-72 * time.Hour)

	db := testdb.OpenStub(t, testdb.SQLStub{
		Query: func(_ context.Context, q string, _ []driver.NamedValue) (driver.Rows, error) {
			if strings.Contains(q, "bp_auth_register_config") {
				return testdb.Rows([]driver.Value{
					int64(62), int64(3), "SVIP", "svip", int64(0),
					int64(0), int64(0), int64(0), int64(0),
					"attempt", int64(0), createdAt, int64(20), "1.5000",
				}), nil
			}
			return testdb.Rows([]driver.Value{
				"GLOBAL:9", "测试套餐", "GLOBAL", int64(1),
				int64(1000), int64(1000), int64(0), int64(1200), "success", "2026-12-01 00:00:00",
				int64(5), int64(0),
			}), nil
		},
	})
	service := &Service{repo: NewRepository(db), rdb: client, log: zap.NewNop(), now: func() time.Time { return now }}

	overview, err := service.QuotaOverview(context.Background(), 62)
	if err != nil {
		t.Fatalf("overview failed: %v", err)
	}
	if len(overview.Free) != 0 {
		t.Fatalf("role quota disabled: free windows must be empty, got %d", len(overview.Free))
	}
	if overview.New != nil {
		t.Fatal("outside the first 24h there must be no new-account window")
	}
	if len(overview.Paid.Packages) != 1 {
		t.Fatalf("expected one paid package, got %d", len(overview.Paid.Packages))
	}
	pkg := overview.Paid.Packages[0]
	if !pkg.IsPrimary || pkg.Remaining != 0 || !pkg.Exhausted {
		t.Fatalf("exhausted package must stay visible and flagged: %+v", pkg)
	}
	if !overview.Paid.Available {
		t.Fatal("points > 0 means a paid path is still available")
	}
}
