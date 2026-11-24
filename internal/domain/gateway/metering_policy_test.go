package gateway

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"
)

// 三维计量策略的可执行规约。核心约束：inherit 必须逐字等于 1.2.88 的判定，
// 因为线上所有接口都还没配策略；只有显式取值才允许改变行为。

func TestMeteringAssetModeDecidesFreeOrCharge(t *testing.T) {
	cases := []struct {
		name     string
		api      InterfaceConfig
		wantFree bool
	}{
		{"inherit zero price is free", InterfaceConfig{Price: "0.0000"}, true},
		{"inherit priced charges", InterfaceConfig{Price: "1.0000"}, false},
		{"inherit priced points charges", InterfaceConfig{Price: "0.0000", PointPrice: 3}, false},
		{"inherit billing disabled never charges", InterfaceConfig{Price: "2.0000", BillingDisabled: true}, true},
		{"free overrides a priced interface", InterfaceConfig{Price: "2.0000", AssetMode: meteringAssetFree}, true},
		{"charge overrides billing disabled", InterfaceConfig{Price: "2.0000", BillingDisabled: true, AssetMode: meteringAssetCharge}, false},
		{"charge with no price cannot charge", InterfaceConfig{Price: "0.0000", AssetMode: meteringAssetCharge}, true},
		{"unknown mode falls back to inherit", InterfaceConfig{Price: "1.0000", AssetMode: "wat"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.api.assetsFree(); got != tc.wantFree {
				t.Fatalf("assetsFree() = %v, want %v for %+v", got, tc.wantFree, tc.api)
			}
		})
	}
}

func TestMeteringChargeIgnoresBillingRules(t *testing.T) {
	// 必扣意味着不看扣费条件；inherit 与 free 沿用「关闭计费即跳过条件判定」。
	if !(InterfaceConfig{Price: "1.0000", AssetMode: meteringAssetCharge}).bypassesBillingRules() {
		t.Fatal("charge mode must bypass billing rules")
	}
	if (InterfaceConfig{Price: "1.0000", BillingRules: []BillingRule{{FieldName: "code"}}}).bypassesBillingRules() {
		t.Fatal("inherit mode with rules must evaluate them")
	}
	if !(InterfaceConfig{Price: "1.0000", AssetMode: meteringAssetFree}).bypassesBillingRules() {
		t.Fatal("free mode never charges, so rules are meaningless")
	}
}

func TestMeteringRoleOverrideResolvesPerDimension(t *testing.T) {
	api := InterfaceConfig{
		Price:       "2.0000",
		AssetMode:   meteringAssetFree,
		PackageMode: meteringPackageSkip,
		GroupMode:   meteringGroupCount,
		RoleMetering: map[int64]InterfaceRoleMetering{
			7: {AssetMode: meteringInherit, PackageMode: meteringPackageConsume, GroupMode: meteringGroupSkip},
		},
	}
	resolved := api.withMeteringFor(7)
	if resolved.AssetMode != meteringAssetFree {
		t.Fatalf("asset override inherit changed it: %q", resolved.AssetMode)
	}
	if resolved.PackageMode != meteringPackageConsume || !resolved.consumesPackageQuota() {
		t.Fatalf("package override must force consumption: %+v", resolved)
	}
	if resolved.GroupMode != meteringGroupSkip || resolved.countsGroupQuota() {
		t.Fatalf("group override must skip counting: %+v", resolved)
	}
	// 未配置覆盖的角色原样继承接口级。
	untouched := api.withMeteringFor(8)
	if untouched.AssetMode != meteringAssetFree || untouched.PackageMode != meteringPackageSkip || untouched.GroupMode != meteringGroupCount {
		t.Fatalf("role without override must inherit: %+v", untouched)
	}
}

func TestMeteringPackageSkipKeepsInterfaceQuotaGate(t *testing.T) {
	skip := InterfaceConfig{PackageMode: meteringPackageSkip}
	if skip.consumesPackageQuota() {
		t.Fatal("package skip must not consume package quota")
	}
	for _, api := range []InterfaceConfig{{}, {PackageMode: meteringInherit}, {PackageMode: meteringPackageConsume}} {
		if !api.consumesPackageQuota() {
			t.Fatalf("inherit/consume must keep consuming packages: %+v", api)
		}
	}
	// 写死不可豁免的两层与 package/group 模式无关：接口全平台总量仍由
	// reserveInterfaceQuota 独立裁决，只受 QuotaEnabled 控制。
	if (InterfaceConfig{PackageMode: meteringPackageSkip, GroupMode: meteringGroupSkip}).QuotaEnabled == 1 {
		t.Fatal("zero-value QuotaEnabled must not look enabled")
	}
}

func TestRequestQuotaWindowsGroupModeSkipsRoleButKeepsNewAccount(t *testing.T) {
	service, _ := newQuotaTestService(t)
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.Local)
	service.now = func() time.Time { return now }
	key := KeyAuth{
		UserID:             61,
		QuotaEnabled:       1,
		QuotaMinuteLimit:   10,
		QuotaHourLimit:     100,
		QuotaDayLimit:      1000,
		QuotaMonthLimit:    10000,
		NewAccountDayLimit: 50,
		UserCreatedAt:      now.Add(-time.Hour),
	}
	inherit := service.requestQuotaWindows(key, InterfaceConfig{})
	if len(inherit) != 5 {
		t.Fatalf("inherit windows = %d, want 4 role windows + new-account: %+v", len(inherit), inherit)
	}
	skipped := service.requestQuotaWindows(key, InterfaceConfig{GroupMode: meteringGroupSkip})
	if len(skipped) != 1 || skipped[0].Name != quotaWindowNewAccount {
		t.Fatalf("group skip must keep only the registration guard: %+v", skipped)
	}
	// 首日额度是注册防刷闸门，任何配置都不得跳过它。
	closed := KeyAuth{UserID: 62, QuotaEnabled: 0, QuotaMinuteLimit: 10, NewAccountDayLimit: 50, UserCreatedAt: now.Add(-time.Hour)}
	if windows := service.requestQuotaWindows(closed, InterfaceConfig{}); len(windows) != 1 {
		t.Fatalf("quota-disabled role keeps only new-account window, got %+v", windows)
	}
	forced := service.requestQuotaWindows(closed, InterfaceConfig{GroupMode: meteringGroupCount})
	if len(forced) != 2 || forced[0].Name != quotaWindowMinute {
		t.Fatalf("group count must build windows even when the role switched quotas off: %+v", forced)
	}
}

func TestMeteringAssetFreeOverridesSavedPriceWithoutReservation(t *testing.T) {
	var api InterfaceConfig
	if err := json.Unmarshal([]byte(`{"ID":5,"APICode":"paid","Price":"3.5000","PointPrice":9,"AssetMode":"free"}`), &api); err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(freeBillingConnector{})
	defer db.Close()
	reservation, err := NewRepository(db).ReserveBilling(context.Background(), api, 2, 1)
	if err != nil {
		t.Fatalf("explicit free must not start a billing transaction: %v", err)
	}
	if reservation.Reserved || reservation.BillingRecordID != 0 || reservation.ChargeType != "NO_CHARGE" {
		t.Fatalf("explicit free reservation = %+v", reservation)
	}
	if api.Price != "3.5000" || api.PointPrice != 9 {
		t.Fatalf("saved pricing must stay untouched: %+v", api)
	}
}
