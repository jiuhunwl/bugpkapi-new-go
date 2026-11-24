package gateway

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

func newDegradeTestService(t *testing.T, now time.Time) (*Service, *miniredis.Miniredis) {
	t.Helper()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	clock := func() time.Time { return now }
	return &Service{
		rdb:            client,
		log:            zap.NewNop(),
		now:            clock,
		interfaceCache: newInterfaceConfigCache(time.Minute),
	}, server
}

func TestDegradeWindowSpecUsesFixedBusinessWindows(t *testing.T) {
	now := time.Date(2026, 11, 11, 23, 30, 0, 0, time.FixedZone("CST", 8*60*60))
	dayBucket, dayTTL, dayRefresh := degradeWindowSpec(now, "day")
	if dayBucket != "20261111" || dayTTL <= 0 || dayRefresh {
		t.Fatalf("day window = %q ttl=%s refresh=%v", dayBucket, dayTTL, dayRefresh)
	}
	monthBucket, monthTTL, monthRefresh := degradeWindowSpec(now, "month")
	if monthBucket != "202611" || monthTTL <= 0 || monthRefresh {
		t.Fatalf("month window = %q ttl=%s refresh=%v", monthBucket, monthTTL, monthRefresh)
	}
	weekBucket, weekTTL, weekRefresh := degradeWindowSpec(now, "week")
	if len(weekBucket) != 8 || weekTTL <= 0 || weekRefresh {
		t.Fatalf("week window = %q ttl=%s refresh=%v", weekBucket, weekTTL, weekRefresh)
	}
	totalBucket, totalTTL, totalRefresh := degradeWindowSpec(now, "total")
	if totalBucket != "total" || totalTTL != degradeTotalTTL || !totalRefresh {
		t.Fatalf("total window = %q ttl=%s refresh=%v", totalBucket, totalTTL, totalRefresh)
	}
}

func TestDegradeCounterIncrementsAndResetsByWindow(t *testing.T) {
	now := time.Date(2026, 11, 11, 10, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	service, _ := newDegradeTestService(t, now)
	api := InterfaceConfig{
		ID:      1,
		APICode: "source",
		Degrade: InterfaceDegradePolicy{Enabled: 1, PolicyVersion: 2},
	}
	rule := InterfaceDegradeRule{Threshold: 3, Window: "day"}
	ctx := context.Background()

	for index := 0; index < 2; index++ {
		service.recordDegradeAttempt(ctx, 9, api, rule)
	}
	count, err := service.degradeCount(ctx, 9, api, rule)
	if err != nil {
		t.Fatalf("count failed: %v", err)
	}
	if count != 2 {
		t.Fatalf("count = %d, want 2", count)
	}
	service.recordDegradeAttempt(ctx, 9, api, rule)
	count, err = service.degradeCount(ctx, 9, api, rule)
	if err != nil {
		t.Fatalf("count failed: %v", err)
	}
	if count != 3 {
		t.Fatalf("count = %d, want 3", count)
	}

	// 新的自然日使用新的 bucket，计数从 0 开始。
	service.now = func() time.Time { return now.AddDate(0, 0, 1) }
	count, err = service.degradeCount(ctx, 9, api, rule)
	if err != nil {
		t.Fatalf("next day count failed: %v", err)
	}
	if count != 0 {
		t.Fatalf("next day count = %d, want 0", count)
	}
}

func TestDegradeCounterIsScopedByPolicyVersion(t *testing.T) {
	now := time.Date(2026, 11, 11, 10, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	service, _ := newDegradeTestService(t, now)
	api := InterfaceConfig{
		ID:      1,
		APICode: "source",
		Degrade: InterfaceDegradePolicy{Enabled: 1, PolicyVersion: 1},
	}
	rule := InterfaceDegradeRule{Threshold: 2, Window: "day"}
	ctx := context.Background()

	service.recordDegradeAttempt(ctx, 9, api, rule)
	if count, _ := service.degradeCount(ctx, 9, api, rule); count != 1 {
		t.Fatalf("version 1 count = %d, want 1", count)
	}
	api.Degrade.PolicyVersion = 2
	if count, _ := service.degradeCount(ctx, 9, api, rule); count != 0 {
		t.Fatalf("version 2 count = %d, want 0", count)
	}
}

func TestResolveDegradeSwitchesToTargetAndDisablesCache(t *testing.T) {
	now := time.Date(2026, 11, 11, 10, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	service, _ := newDegradeTestService(t, now)
	target := InterfaceConfig{
		ID:           2,
		APICode:      "target",
		Status:       1,
		CacheEnabled: 1,
		RequestURL:   "https://target.example/api",
	}
	service.interfaceCache.set(now, target.APICode, target)
	api := InterfaceConfig{
		ID:      1,
		APICode: "source",
		Degrade: InterfaceDegradePolicy{
			Enabled:                  1,
			DefaultTargetInterfaceID: 2,
			DefaultTargetAPICode:     "target",
			PolicyVersion:            1,
			RoleRules:                map[int64]InterfaceDegradeRule{1: {Threshold: 2, Window: "day"}},
		},
	}
	key := KeyAuth{UserID: 9, RoleID: 1, RoleKey: "free"}
	ctx := context.Background()

	service.recordDegradeAttempt(ctx, 9, api, api.Degrade.RoleRules[1])
	service.recordDegradeAttempt(ctx, 9, api, api.Degrade.RoleRules[1])
	execAPI, rule, configured, degraded := service.resolveDegradeExecution(ctx, key, api, OpenRequest{})
	if !configured || rule.Threshold != 2 {
		t.Fatalf("configured=%v rule=%+v", configured, rule)
	}
	if !degraded {
		t.Fatal("counter reached threshold but degrade was not triggered")
	}
	if execAPI.ID != 2 || execAPI.CacheEnabled != 0 {
		t.Fatalf("execution api = id:%d cache:%d", execAPI.ID, execAPI.CacheEnabled)
	}
}

func TestResolveDegradeUsesCustomInternalTargetURL(t *testing.T) {
	now := time.Date(2026, 11, 11, 10, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	service, _ := newDegradeTestService(t, now)
	api := InterfaceConfig{
		ID:             1,
		APICode:        "source",
		RequestURL:     "https://public.example/api",
		CacheEnabled:   1,
		PollingEnabled: 1,
		PollingMode:    "ROUND_ROBIN",
		UpstreamConfigs: []UpstreamConfig{
			{URL: "https://node-a.example/api"},
			{URL: "https://node-b.example/api"},
		},
		Degrade: InterfaceDegradePolicy{
			Enabled:          1,
			DefaultTargetURL: "http://10.0.0.8:8080/internal/{text}",
			PolicyVersion:    1,
			RoleRules:        map[int64]InterfaceDegradeRule{1: {Threshold: 1, Window: "day"}},
		},
	}
	key := KeyAuth{UserID: 9, RoleID: 1, RoleKey: "free"}
	ctx := context.Background()
	service.recordDegradeAttempt(ctx, 9, api, api.Degrade.RoleRules[1])
	execAPI, _, configured, degraded := service.resolveDegradeExecution(ctx, key, api, OpenRequest{})
	if !configured || !degraded {
		t.Fatalf("configured=%v degraded=%v", configured, degraded)
	}
	if execAPI.RequestURL != "http://10.0.0.8:8080/internal/{text}" {
		t.Fatalf("custom target url = %q", execAPI.RequestURL)
	}
	if execAPI.PollingEnabled != 0 || execAPI.PollingMode != "SINGLE" ||
		len(execAPI.UpstreamConfigs) != 0 || execAPI.CacheEnabled != 0 {
		t.Fatalf("custom target must use a single upstream and disable cache: %+v", execAPI)
	}
	if execAPI.degradeTargetKind != "custom" {
		t.Fatalf("degrade target kind = %q", execAPI.degradeTargetKind)
	}
}

func TestResolveDegradeRuleInterfaceTargetOverridesDefaultCustomURL(t *testing.T) {
	now := time.Date(2026, 11, 11, 10, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	service, _ := newDegradeTestService(t, now)
	target := InterfaceConfig{
		ID:           2,
		APICode:      "target",
		Status:       1,
		RequestURL:   "https://target.example/api",
		CacheEnabled: 1,
	}
	service.interfaceCache.set(now, target.APICode, target)
	api := InterfaceConfig{
		ID:      1,
		APICode: "source",
		Degrade: InterfaceDegradePolicy{
			Enabled:          1,
			DefaultTargetURL: "http://10.0.0.8/internal",
			PolicyVersion:    1,
			RoleRules: map[int64]InterfaceDegradeRule{1: {
				Threshold: 1, Window: "day",
				TargetInterfaceID: 2, TargetAPICode: "target",
			}},
		},
	}
	key := KeyAuth{UserID: 9, RoleID: 1, RoleKey: "free"}
	ctx := context.Background()
	service.recordDegradeAttempt(ctx, 9, api, api.Degrade.RoleRules[1])
	execAPI, _, configured, degraded := service.resolveDegradeExecution(ctx, key, api, OpenRequest{})
	if !configured || !degraded {
		t.Fatalf("configured=%v degraded=%v", configured, degraded)
	}
	if execAPI.ID != 2 || execAPI.degradeTargetKind != "interface" {
		t.Fatalf("rule-level interface target lost: id=%d kind=%q", execAPI.ID, execAPI.degradeTargetKind)
	}
}

func TestResolveDegradeFailsOpenWhenCounterUnavailable(t *testing.T) {
	now := time.Date(2026, 11, 11, 10, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	service, server := newDegradeTestService(t, now)
	server.Close()
	api := InterfaceConfig{
		ID:      1,
		APICode: "source",
		Degrade: InterfaceDegradePolicy{
			Enabled:       1,
			PolicyVersion: 1,
			RoleRules:     map[int64]InterfaceDegradeRule{1: {Threshold: 1, Window: "day"}},
		},
	}
	key := KeyAuth{UserID: 9, RoleID: 1, RoleKey: "free"}
	execAPI, rule, configured, degraded := service.resolveDegradeExecution(context.Background(), key, api, OpenRequest{})
	if !configured || rule.Threshold != 1 {
		t.Fatalf("configured=%v rule=%+v", configured, rule)
	}
	if degraded || execAPI.ID != api.ID {
		t.Fatalf("unavailable counter must fail open: degraded=%v execID=%d", degraded, execAPI.ID)
	}
}

func TestResolveDegradeSkipsAdminAndUnconfiguredRoles(t *testing.T) {
	now := time.Date(2026, 11, 11, 10, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	service, _ := newDegradeTestService(t, now)
	api := InterfaceConfig{
		ID:      1,
		APICode: "source",
		Degrade: InterfaceDegradePolicy{
			Enabled:       1,
			PolicyVersion: 1,
			RoleRules:     map[int64]InterfaceDegradeRule{1: {Threshold: 1, Window: "day"}},
		},
	}
	ctx := context.Background()
	if _, _, configured, degraded := service.resolveDegradeExecution(ctx, KeyAuth{UserID: 9, RoleID: 1, RoleKey: "admin"}, api, OpenRequest{}); configured || degraded {
		t.Fatal("admin must not be degraded")
	}
	if _, _, configured, degraded := service.resolveDegradeExecution(ctx, KeyAuth{UserID: 9, RoleID: 2, RoleKey: "free"}, api, OpenRequest{}); configured || degraded {
		t.Fatal("unconfigured role must not be degraded")
	}
}
