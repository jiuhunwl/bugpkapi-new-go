package gateway

import (
	"context"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"
	"time"

	"bugpk-api/internal/pkg/testdb"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

// 免费额度（用户组配额）必须先被消费，打满之后才由付费额度承接。
// 这条断言的是「两层分清」：有套餐不等于免费额度不存在。
func TestReserveRequestQuotaConsumesFreeQuotaBeforePaidEntitlement(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	db := testdb.OpenStub(t, testdb.SQLStub{
		Query: func(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
			// HasPaidEntitlement 是单行 exists 聚合，任何 1 都代表「有可用付费途径」。
			return testdb.Rows([]driver.Value{int64(1)}), nil
		},
	})
	service := &Service{repo: NewRepository(db), rdb: client, log: zap.NewNop(), now: time.Now}
	key := KeyAuth{
		UserID:           51,
		RoleKey:          "user",
		QuotaEnabled:     1,
		QuotaMinuteLimit: 1,
		QuotaCountMode:   quotaCountModeAttempt,
	}
	api := InterfaceConfig{ID: 9, APICode: "paid", Price: "1.0000"}
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		if _, err := service.reserveRequestQuota(ctx, key, api); err != nil {
			t.Fatalf("request %d must pass (free quota first, paid after): %v", i+1, err)
		}
	}
	// 只有第一次消耗免费额度，其余四次由付费额度承接，不再回写免费计数。
	minuteKeys := 0
	for _, k := range server.Keys() {
		if !strings.Contains(k, ":minute:") {
			continue
		}
		minuteKeys++
		got, err := server.Get(k)
		if err != nil {
			t.Fatalf("read counter %q: %v", k, err)
		}
		if got != "1" {
			t.Fatalf("free quota counter must stay at 1 after paid handover, got %q", got)
		}
	}
	if minuteKeys != 1 {
		t.Fatalf("expected exactly one minute bucket, got %d", minuteKeys)
	}
}

// 免费额度打满且没有任何付费途径时，收费接口同样必须 429，而不是被放行。
func TestReserveRequestQuotaRejectsWhenFreeExhaustedWithoutPaid(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	probes := 0
	db := testdb.OpenStub(t, testdb.SQLStub{
		Query: func(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
			probes++
			return testdb.Rows([]driver.Value{int64(0)}), nil
		},
	})
	service := &Service{repo: NewRepository(db), rdb: client, log: zap.NewNop(), now: time.Now}
	key := KeyAuth{
		UserID:           53,
		RoleKey:          "user",
		QuotaEnabled:     1,
		QuotaMinuteLimit: 1,
		QuotaCountMode:   quotaCountModeAttempt,
	}
	api := InterfaceConfig{ID: 11, APICode: "paid", Price: "1.0000"}
	ctx := context.Background()
	if _, err := service.reserveRequestQuota(ctx, key, api); err != nil {
		t.Fatalf("first request should consume the free quota: %v", err)
	}
	if _, err := service.reserveRequestQuota(ctx, key, api); err == nil {
		t.Fatal("second request must be rejected once free quota is exhausted and no paid path exists")
	}
	if probes != 1 {
		t.Fatalf("paid entitlement must be probed exactly once after free quota ran out, got %d", probes)
	}
}

// 付费探测失败不能让请求穿过闸门：DB 抖动不能变成绕过免费额度的口子。
func TestReserveRequestQuotaKeepsFreeLimitWhenProbeFails(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	db := testdb.OpenStub(t, testdb.SQLStub{
		Query: func(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
			return nil, errors.New("db down")
		},
	})
	service := &Service{repo: NewRepository(db), rdb: client, log: zap.NewNop(), now: time.Now}
	key := KeyAuth{
		UserID:           54,
		RoleKey:          "user",
		QuotaEnabled:     1,
		QuotaMinuteLimit: 1,
		QuotaCountMode:   quotaCountModeAttempt,
	}
	api := InterfaceConfig{ID: 12, APICode: "paid", Price: "1.0000"}
	ctx := context.Background()
	if _, err := service.reserveRequestQuota(ctx, key, api); err != nil {
		t.Fatalf("first request should consume the free quota: %v", err)
	}
	if _, err := service.reserveRequestQuota(ctx, key, api); err == nil {
		t.Fatal("probe failure must not bypass the free quota")
	}
}

// 免费接口没有可扣的付费途径，探测会让套餐用户对免费接口无限调用，
// 因此免费接口必须继续受配额约束，也不允许触发权益探测查询。
func TestReserveRequestQuotaKeepsFreeInterfaceLimited(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	db := testdb.OpenStub(t, testdb.SQLStub{
		Query: func(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
			return nil, errors.New("free interface must not probe paid entitlements")
		},
	})
	service := &Service{repo: NewRepository(db), rdb: client, log: zap.NewNop(), now: time.Now}
	key := KeyAuth{
		UserID:           52,
		RoleKey:          "user",
		QuotaEnabled:     1,
		QuotaMinuteLimit: 2,
		QuotaCountMode:   quotaCountModeAttempt,
	}
	api := InterfaceConfig{ID: 10, APICode: "free", Price: "0.0000"}
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if _, err := service.reserveRequestQuota(ctx, key, api); err != nil {
			t.Fatalf("free request %d should pass: %v", i+1, err)
		}
	}
	if _, err := service.reserveRequestQuota(ctx, key, api); err == nil {
		t.Fatal("free interface must still be limited by the role quota")
	}
}

// 「仅成功计量」模式下，付费承接的 reservation 不能带窗口，否则成功后会把
// 已经打满的免费额度再补记一次。
func TestReserveRequestQuotaSuccessModePaidHandoverCarriesNoWindows(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	db := testdb.OpenStub(t, testdb.SQLStub{
		Query: func(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
			return testdb.Rows([]driver.Value{int64(1)}), nil
		},
	})
	service := &Service{repo: NewRepository(db), rdb: client, log: zap.NewNop(), now: time.Now}
	key := KeyAuth{
		UserID:           55,
		RoleKey:          "user",
		QuotaEnabled:     1,
		QuotaMinuteLimit: 1,
		QuotaCountMode:   quotaCountModeSuccess,
	}
	api := InterfaceConfig{ID: 13, APICode: "paid", Price: "1.0000"}
	ctx := context.Background()

	first, err := service.reserveRequestQuota(ctx, key, api)
	if err != nil {
		t.Fatalf("first request should pass: %v", err)
	}
	if len(first.Windows) != 1 {
		t.Fatalf("free-quota reservation must carry its windows, got %d", len(first.Windows))
	}
	service.commitRequestQuota(ctx, first)

	second, err := service.reserveRequestQuota(ctx, key, api)
	if err != nil {
		t.Fatalf("paid handover must pass: %v", err)
	}
	if len(second.Windows) != 0 {
		t.Fatalf("paid handover must not commit free quota, got %d windows", len(second.Windows))
	}
}

// 付费探测有短 TTL 缓存：免费额度打满后，同一用户同一接口在缓存期内只查一次库，
// 过期后必须重新探测，购买套餐或充值才能及时生效。
func TestPaidEntitlementProbeIsCachedWithShortTTL(t *testing.T) {
	current := time.Date(2026, 11, 4, 14, 0, 0, 0, time.UTC)
	probes := 0
	db := testdb.OpenStub(t, testdb.SQLStub{
		Query: func(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
			probes++
			return testdb.Rows([]driver.Value{int64(1)}), nil
		},
	})
	service := &Service{repo: NewRepository(db), log: zap.NewNop(), now: func() time.Time { return current }}
	ctx := context.Background()

	if !service.paidEntitlementAvailable(ctx, 88, 7) {
		t.Fatal("stub returns 1, entitlement must be available")
	}
	if !service.paidEntitlementAvailable(ctx, 88, 7) {
		t.Fatal("cached result must stay true")
	}
	if probes != 1 {
		t.Fatalf("second call must hit the cache, probes=%d", probes)
	}

	current = current.Add(paidEntitlementPositiveTTL + time.Second)
	service.paidEntitlementAvailable(ctx, 88, 7)
	if probes != 2 {
		t.Fatalf("expired entry must be re-probed, probes=%d", probes)
	}
}

// 负结果只缓存 5 秒：刚买完套餐的用户不应该被旧判定拦住太久。
func TestPaidEntitlementNegativeCacheExpiresQuickly(t *testing.T) {
	current := time.Date(2026, 11, 4, 14, 0, 0, 0, time.UTC)
	available := int64(0)
	probes := 0
	db := testdb.OpenStub(t, testdb.SQLStub{
		Query: func(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
			probes++
			return testdb.Rows([]driver.Value{available}), nil
		},
	})
	service := &Service{repo: NewRepository(db), log: zap.NewNop(), now: func() time.Time { return current }}
	ctx := context.Background()

	if service.paidEntitlementAvailable(ctx, 89, 7) {
		t.Fatal("stub returns 0, entitlement must be unavailable")
	}
	current = current.Add(paidEntitlementNegativeTTL + time.Second)
	available = 1
	if !service.paidEntitlementAvailable(ctx, 89, 7) {
		t.Fatal("after the negative TTL the probe must run again and see the new package")
	}
	if probes != 2 {
		t.Fatalf("expected 2 probes, got %d", probes)
	}
}

// 未指定主套餐时 primaryPackageScope 返回空串，网关按「全站套餐优先」处理。
func TestPrimaryPackageScopeDefaultsToEmpty(t *testing.T) {
	db := testdb.OpenStub(t, testdb.SQLStub{
		Query: func(_ context.Context, q string, _ []driver.NamedValue) (driver.Rows, error) {
			return testdb.Rows([]driver.Value{""}), nil
		},
	})
	scope, err := primaryPackageScope(context.Background(), db, 77)
	if err != nil {
		t.Fatalf("scope probe failed: %v", err)
	}
	if scope != "" {
		t.Fatalf("expected empty scope, got %q", scope)
	}
}
