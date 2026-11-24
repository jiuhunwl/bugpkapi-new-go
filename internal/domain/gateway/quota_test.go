package gateway

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

func newQuotaTestService(t *testing.T) (*Service, *miniredis.Miniredis) {
	t.Helper()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return &Service{rdb: client, log: zap.NewNop(), now: time.Now}, server
}

func TestReserveRequestQuotaAttemptModeConsumesAndRejects(t *testing.T) {
	service, _ := newQuotaTestService(t)
	key := KeyAuth{
		UserID:           41,
		RoleKey:          "user",
		QuotaEnabled:     1,
		QuotaMinuteLimit: 2,
		QuotaHourLimit:   10,
		QuotaDayLimit:    100,
		QuotaMonthLimit:  1000,
		QuotaCountMode:   quotaCountModeAttempt,
	}
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if _, err := service.reserveRequestQuota(ctx, key, InterfaceConfig{ID: 1, Price: "1.0000"}); err != nil {
			t.Fatalf("request %d should pass: %v", i+1, err)
		}
	}
	if _, err := service.reserveRequestQuota(ctx, key, InterfaceConfig{ID: 1, Price: "1.0000"}); err == nil {
		t.Fatal("third request must be rejected by the minute quota")
	}
}

func TestReserveRequestQuotaSuccessModeOnlyCountsOnCommit(t *testing.T) {
	service, _ := newQuotaTestService(t)
	key := KeyAuth{
		UserID:           42,
		RoleKey:          "user",
		QuotaEnabled:     1,
		QuotaMinuteLimit: 1,
		QuotaCountMode:   quotaCountModeSuccess,
	}
	ctx := context.Background()
	reservation, err := service.reserveRequestQuota(ctx, key, InterfaceConfig{ID: 1, Price: "1.0000"})
	if err != nil {
		t.Fatalf("first check failed: %v", err)
	}
	// 未 commit 前失败请求不占额度。
	if _, err := service.reserveRequestQuota(ctx, key, InterfaceConfig{ID: 1, Price: "1.0000"}); err != nil {
		t.Fatalf("uncommitted quota must not block: %v", err)
	}
	service.commitRequestQuota(ctx, reservation)
	if _, err := service.reserveRequestQuota(ctx, key, InterfaceConfig{ID: 1, Price: "1.0000"}); err == nil {
		t.Fatal("after one successful commit the limit must be exhausted")
	}
}

func TestReserveRequestQuotaNewAccountWindowAndExpiry(t *testing.T) {
	service, _ := newQuotaTestService(t)
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.Local)
	service.now = func() time.Time { return now }
	key := KeyAuth{
		UserID:             43,
		RoleKey:            "user",
		QuotaEnabled:       1,
		QuotaCountMode:     quotaCountModeAttempt,
		NewAccountDayLimit: 1,
		UserCreatedAt:      now.Add(-time.Hour),
	}
	ctx := context.Background()
	if _, err := service.reserveRequestQuota(ctx, key, InterfaceConfig{ID: 1, Price: "1.0000"}); err != nil {
		t.Fatalf("first new-account call should pass: %v", err)
	}
	if _, err := service.reserveRequestQuota(ctx, key, InterfaceConfig{ID: 1, Price: "1.0000"}); err == nil {
		t.Fatal("second new-account call must be rejected by the first-day cap")
	}
	// 24h 之后首日窗口消失，只剩角色窗口（此处角色窗口为 0 = 不限）。
	service.now = func() time.Time { return now.Add(25 * time.Hour) }
	if _, err := service.reserveRequestQuota(ctx, key, InterfaceConfig{ID: 1, Price: "1.0000"}); err != nil {
		t.Fatalf("quota must lift after 24h: %v", err)
	}
}

func TestReserveRequestQuotaSkipsAdminAndDisabledRoles(t *testing.T) {
	service, _ := newQuotaTestService(t)
	ctx := context.Background()
	for _, key := range []KeyAuth{
		{UserID: 44, RoleKey: "admin", QuotaEnabled: 1, QuotaMinuteLimit: 1},
		{UserID: 45, RoleKey: "user", QuotaEnabled: 0, QuotaMinuteLimit: 1},
	} {
		for i := 0; i < 3; i++ {
			if _, err := service.reserveRequestQuota(ctx, key, InterfaceConfig{ID: 1, Price: "1.0000"}); err != nil {
				t.Fatalf("bypass role should not be limited: %v", err)
			}
		}
	}
}

func TestReserveRequestQuotaNewAccountAppliesWhenRoleQuotaDisabled(t *testing.T) {
	service, _ := newQuotaTestService(t)
	now := time.Now()
	service.now = func() time.Time { return now }
	key := KeyAuth{
		UserID:             47,
		RoleKey:            "vip",
		QuotaEnabled:       0,
		QuotaCountMode:     quotaCountModeAttempt,
		NewAccountDayLimit: 1,
		UserCreatedAt:      now.Add(-time.Hour),
	}
	ctx := context.Background()
	if _, err := service.reserveRequestQuota(ctx, key, InterfaceConfig{ID: 1, Price: "1.0000"}); err != nil {
		t.Fatalf("first call should pass: %v", err)
	}
	if _, err := service.reserveRequestQuota(ctx, key, InterfaceConfig{ID: 1, Price: "1.0000"}); err == nil {
		t.Fatal("first-day cap must apply even when role quota is disabled")
	}
}

func TestInterfaceQuotaPerUserAndAllUsersLimits(t *testing.T) {
	service, _ := newQuotaTestService(t)
	api := InterfaceConfig{
		ID:             90,
		APICode:        "iface_quota",
		QuotaEnabled:   1,
		QuotaCountMode: quotaCountModeAttempt,
		QuotaPerUser:   InterfaceWindowLimits{Hour: 1},
		QuotaAllUsers:  InterfaceWindowLimits{Hour: 100},
	}
	ctx := context.Background()
	first := KeyAuth{UserID: 501, RoleID: 2, RoleKey: "user"}
	if _, err := service.reserveInterfaceQuota(ctx, first, api, false); err != nil {
		t.Fatalf("first per-user call should pass: %v", err)
	}
	if _, err := service.reserveInterfaceQuota(ctx, first, api, false); err == nil {
		t.Fatal("second per-user call must be rejected")
	}
	// 另一个账号仍然可用（全平台总量未到）。
	second := KeyAuth{UserID: 502, RoleID: 2, RoleKey: "user"}
	if _, err := service.reserveInterfaceQuota(ctx, second, api, false); err != nil {
		t.Fatalf("another user should not be blocked by the first user's cap: %v", err)
	}
	// 全平台总量单独生效（换一个接口，避免复用上面的全平台计数）。
	api.ID = 93
	api.QuotaPerUser = InterfaceWindowLimits{}
	api.QuotaAllUsers = InterfaceWindowLimits{Hour: 2}
	if _, err := service.reserveInterfaceQuota(ctx, KeyAuth{UserID: 503, RoleID: 2}, api, false); err != nil {
		t.Fatalf("first all-user call should pass: %v", err)
	}
	if _, err := service.reserveInterfaceQuota(ctx, KeyAuth{UserID: 504, RoleID: 2}, api, false); err != nil {
		t.Fatalf("second all-user call should pass: %v", err)
	}
	if _, err := service.reserveInterfaceQuota(ctx, KeyAuth{UserID: 505, RoleID: 2}, api, false); err == nil {
		t.Fatal("all-user cap must reject the third account")
	}
}

// 套餐豁免（默认 cover_package=0）：套餐承接的请求整层跳过接口配额，
// 不检查也不计数；开启覆盖后，套餐承接的请求同样受限。
func TestInterfaceQuotaPackageExempt(t *testing.T) {
	service, _ := newQuotaTestService(t)
	ctx := context.Background()
	key := KeyAuth{UserID: 601, RoleID: 2, RoleKey: "user"}
	api := InterfaceConfig{
		ID:             94,
		APICode:        "iface_exempt",
		QuotaEnabled:   1,
		QuotaCountMode: quotaCountModeAttempt,
		QuotaPerUser:   InterfaceWindowLimits{Day: 1},
	}
	for i := 0; i < 3; i++ {
		reservation, err := service.reserveInterfaceQuota(ctx, key, api, true)
		if err != nil {
			t.Fatalf("package-covered call %d must be exempt: %v", i+1, err)
		}
		if reservation.Mode != "" || len(reservation.Windows) != 0 {
			t.Fatalf("exempt call must not create reservation: %+v", reservation)
		}
	}
	// 豁免期间的调用没有消耗接口计数：第一条免费调用仍应放行。
	if _, err := service.reserveInterfaceQuota(ctx, key, api, false); err != nil {
		t.Fatalf("first non-package call should pass: %v", err)
	}
	if _, err := service.reserveInterfaceQuota(ctx, key, api, false); err == nil {
		t.Fatal("second non-package call must be rejected by the per-user cap")
	}
	// 开启覆盖：套餐承接的请求也要受同一上限约束。
	api.QuotaCoverPackage = 1
	if _, err := service.reserveInterfaceQuota(ctx, key, api, true); err == nil {
		t.Fatal("when cover_package=1, package-covered calls must be limited too")
	}
}

func TestInterfaceQuotaRoleOverrideUnlimitedAndAdjust(t *testing.T) {
	base := InterfaceWindowLimits{Hour: 10, Day: 100, Week: 500, Month: 1000}
	unlimited := effectiveInterfaceUserLimits(base, InterfaceRoleQuota{Mode: "unlimited"})
	if unlimited != (InterfaceWindowLimits{}) {
		t.Fatalf("unlimited override = %+v", unlimited)
	}
	adjusted := effectiveInterfaceUserLimits(base, InterfaceRoleQuota{
		Mode:   "adjust",
		Adjust: InterfaceWindowLimits{Hour: 5, Day: -50, Week: -5000, Month: 10},
	})
	if adjusted.Hour != 15 || adjusted.Day != 50 || adjusted.Week != 1 || adjusted.Month != 1010 {
		t.Fatalf("adjust override = %+v", adjusted)
	}
	override := effectiveInterfaceUserLimits(base, InterfaceRoleQuota{
		Mode:     "override",
		Override: InterfaceWindowLimits{Hour: 1, Day: 2, Week: 3, Month: 4},
	})
	if override.Hour != 1 || override.Day != 2 || override.Week != 3 || override.Month != 4 {
		t.Fatalf("override = %+v", override)
	}
	// 默认值本身是不限时，负向调整不能把一个不限量接口变成有限量。
	stillUnlimited := effectiveInterfaceUserLimits(InterfaceWindowLimits{}, InterfaceRoleQuota{
		Mode:   "adjust",
		Adjust: InterfaceWindowLimits{Hour: -10},
	})
	if stillUnlimited != (InterfaceWindowLimits{}) {
		t.Fatalf("unlimited base + adjust = %+v", stillUnlimited)
	}
}

func TestInterfaceQuotaSuccessModeCountsOnlyOnCommit(t *testing.T) {
	service, _ := newQuotaTestService(t)
	api := InterfaceConfig{
		ID:             91,
		APICode:        "iface_success",
		QuotaEnabled:   1,
		QuotaCountMode: quotaCountModeSuccess,
		QuotaPerUser:   InterfaceWindowLimits{Day: 1},
	}
	key := KeyAuth{UserID: 505, RoleID: 2}
	ctx := context.Background()
	reservation, err := service.reserveInterfaceQuota(ctx, key, api, false)
	if err != nil {
		t.Fatalf("first check failed: %v", err)
	}
	if _, err := service.reserveInterfaceQuota(ctx, key, api, false); err != nil {
		t.Fatalf("uncommitted success quota must not block: %v", err)
	}
	service.commitRequestQuota(ctx, reservation)
	if _, err := service.reserveInterfaceQuota(ctx, key, api, false); err == nil {
		t.Fatal("after one successful interface call the daily cap must be exhausted")
	}
}

func TestInterfaceQuotaWindowsIncludeWeek(t *testing.T) {
	service, _ := newQuotaTestService(t)
	now := time.Date(2026, 11, 5, 10, 0, 0, 0, time.Local)
	service.now = func() time.Time { return now }
	windows := service.interfaceQuotaWindows(92, 506,
		InterfaceWindowLimits{Hour: 1, Day: 2, Week: 3, Month: 4},
		InterfaceWindowLimits{Hour: 5, Day: 6, Week: 7, Month: 8},
	)
	if len(windows) != 8 {
		t.Fatalf("windows = %d, want 8", len(windows))
	}
	var weekSeen bool
	for _, window := range windows {
		if strings.Contains(window.Key, ":week:") {
			weekSeen = true
		}
		if window.TTL <= 0 {
			t.Fatalf("window %s has non-positive TTL: %v", window.Key, window.TTL)
		}
	}
	if !weekSeen {
		t.Fatal("weekly interface quota window missing")
	}
}

// ignore 是「整层跳过」：该用户组既不检查也不计数，连全平台总量都不占。
// 这正是它与 unlimited 的分界——unlimited 只放开单用户上限，全平台计数照常消耗。
func TestInterfaceQuotaRoleIgnoreSkipsEntireLayer(t *testing.T) {
	service, server := newQuotaTestService(t)
	ctx := context.Background()
	api := InterfaceConfig{
		ID:             95,
		APICode:        "iface_ignore",
		QuotaEnabled:   1,
		QuotaCountMode: quotaCountModeAttempt,
		QuotaPerUser:   InterfaceWindowLimits{Day: 1},
		QuotaAllUsers:  InterfaceWindowLimits{Day: 2},
		RoleQuotas: map[int64]InterfaceRoleQuota{
			3: {Mode: "ignore"},
		},
	}
	ignored := KeyAuth{UserID: 701, RoleID: 3, RoleKey: "internal"}
	for i := 0; i < 5; i++ {
		reservation, err := service.reserveInterfaceQuota(ctx, ignored, api, false)
		if err != nil {
			t.Fatalf("ignore role call %d must pass: %v", i+1, err)
		}
		if reservation.Mode != "" || len(reservation.Windows) != 0 {
			t.Fatalf("ignore role must not create a reservation: %+v", reservation)
		}
	}
	// 关键断言：被忽略的调用一个计数键都没写，全平台额度池保持干净。
	for _, k := range server.Keys() {
		t.Fatalf("ignore role leaked a counter: %s", k)
	}
	// 普通角色没有单用户上限，只受全平台总量 2 次约束；它的第一次调用必须成功，
	// 证明额度池没有被 ignore 的 5 次流量吃掉。
	api.QuotaPerUser = InterfaceWindowLimits{}
	norole := KeyAuth{UserID: 702, RoleID: 4, RoleKey: "user"}
	for i := 0; i < 2; i++ {
		if _, err := service.reserveInterfaceQuota(ctx, norole, api, false); err != nil {
			t.Fatalf("normal call %d must pass: %v", i+1, err)
		}
	}
	if _, err := service.reserveInterfaceQuota(ctx, norole, api, false); err == nil {
		t.Fatal("platform total must still cap normal roles")
	}
}

// unlimited 与 ignore 的对照：unlimited 仍然写全平台计数。
func TestInterfaceQuotaRoleUnlimitedStillConsumesAllUsersWindow(t *testing.T) {
	service, server := newQuotaTestService(t)
	ctx := context.Background()
	api := InterfaceConfig{
		ID:             96,
		APICode:        "iface_unlimited_vs_ignore",
		QuotaEnabled:   1,
		QuotaCountMode: quotaCountModeAttempt,
		QuotaPerUser:   InterfaceWindowLimits{Day: 1},
		QuotaAllUsers:  InterfaceWindowLimits{Day: 2},
		RoleQuotas: map[int64]InterfaceRoleQuota{
			5: {Mode: "unlimited"},
		},
	}
	unlimited := KeyAuth{UserID: 703, RoleID: 5, RoleKey: "vip"}
	// unlimited 放开单用户上限，所以第 1、2 次只受全平台总量（2 次）约束。
	for i := 0; i < 2; i++ {
		if _, err := service.reserveInterfaceQuota(ctx, unlimited, api, false); err != nil {
			t.Fatalf("unlimited call %d must pass the per-user cap: %v", i+1, err)
		}
	}
	if _, err := service.reserveInterfaceQuota(ctx, unlimited, api, false); err == nil {
		t.Fatal("unlimited must still be capped by the platform total")
	}
	wroteAllUsers := false
	for _, k := range server.Keys() {
		if strings.Contains(k, ":all:") {
			wroteAllUsers = true
		}
	}
	if !wroteAllUsers {
		t.Fatal("unlimited must consume the platform-wide counter, unlike ignore")
	}
}

func TestRequestQuotaWindowsUseTieredBuckets(t *testing.T) {
	service, _ := newQuotaTestService(t)
	now := time.Date(2026, 11, 30, 23, 59, 30, 0, time.Local)
	service.now = func() time.Time { return now }
	windows := service.requestQuotaWindows(KeyAuth{
		UserID:           46,
		QuotaEnabled:     1,
		QuotaMinuteLimit: 10,
		QuotaHourLimit:   100,
		QuotaDayLimit:    1000,
		QuotaMonthLimit:  10000,
	}, InterfaceConfig{})
	if len(windows) != 4 {
		t.Fatalf("windows = %d, want 4", len(windows))
	}
	if windows[0].Name != quotaWindowMinute || windows[1].Name != quotaWindowHour ||
		windows[2].Name != quotaWindowDay || windows[3].Name != quotaWindowMonth {
		t.Fatalf("unexpected window order: %+v", windows)
	}
	for _, window := range windows {
		if window.TTL <= 0 {
			t.Fatalf("window %s has non-positive TTL: %v", window.Name, window.TTL)
		}
	}
}
