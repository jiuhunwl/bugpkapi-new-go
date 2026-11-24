package interfaceapi

import (
	"strings"
	"testing"
)

func TestNormalizeInterfaceMeteringDefaultsToInherit(t *testing.T) {
	policy, err := normalizeInterfaceMetering(InterfaceMeteringPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	if policy.AssetMode != meteringInherit || policy.PackageMode != meteringInherit || policy.GroupMode != meteringInherit {
		t.Fatalf("empty request must normalize to inherit: %+v", policy)
	}
	if len(policy.RoleOverrides) != 0 {
		t.Fatalf("all-inherit overrides must be dropped: %+v", policy.RoleOverrides)
	}
}

func TestNormalizeInterfaceMeteringRejectsUnknownModes(t *testing.T) {
	cases := []InterfaceMeteringPolicy{
		{AssetMode: "skip"},
		{PackageMode: "free"},
		{GroupMode: "consume"},
		{RoleOverrides: []InterfaceRoleMetering{{RoleID: 3, AssetMode: "nope"}}},
		{RoleOverrides: []InterfaceRoleMetering{{RoleID: 0, PackageMode: meteringSkip}}},
		{RoleOverrides: []InterfaceRoleMetering{{RoleID: 3, PackageMode: meteringSkip}, {RoleID: 3, GroupMode: meteringSkip}}},
	}
	for index, req := range cases {
		if _, err := normalizeInterfaceMetering(req); err == nil {
			t.Fatalf("case %d must be rejected: %+v", index, req)
		}
	}
}

func TestRequireUnmeteredGuardBlocksFullyUnmeteredWithoutPlatformCap(t *testing.T) {
	unmetered := InterfaceMeteringPolicy{AssetMode: meteringAssetFree, PackageMode: meteringSkip, GroupMode: meteringSkip}
	if err := requireUnmeteredGuard(unmetered, InterfaceQuotaPolicy{}); err == nil {
		t.Fatal("fully unmetered policy without any platform cap must be rejected")
	}
	if err := requireUnmeteredGuard(unmetered, InterfaceQuotaPolicy{Enabled: 1}); err == nil {
		t.Fatal("enabled quota without a platform cap is still no guard")
	}
	if err := requireUnmeteredGuard(unmetered, InterfaceQuotaPolicy{Enabled: 1, AllUsersDay: 1000}); err != nil {
		t.Fatalf("platform cap is the required fallback: %v", err)
	}
	// 只要还有一层计量在起作用，就不该拦。
	partial := InterfaceMeteringPolicy{AssetMode: meteringAssetFree, PackageMode: meteringSkip, GroupMode: meteringInherit}
	if err := requireUnmeteredGuard(partial, InterfaceQuotaPolicy{}); err != nil {
		t.Fatalf("partial unmetering must pass: %v", err)
	}
	// 用户组覆盖解析后同样受检查：接口级只关了套餐，组级再关掉另外两维也等于完全免计量。
	viaOverride := InterfaceMeteringPolicy{
		PackageMode: meteringSkip,
		RoleOverrides: []InterfaceRoleMetering{
			{RoleID: 9, AssetMode: meteringAssetFree, GroupMode: meteringSkip},
		},
	}
	if err := requireUnmeteredGuard(viaOverride, InterfaceQuotaPolicy{}); err == nil {
		t.Fatal("role override resolving to fully unmetered must be rejected")
	}
	if err := requireUnmeteredGuard(viaOverride, InterfaceQuotaPolicy{Enabled: 1, AllUsersHour: 10}); err != nil {
		t.Fatalf("override with platform cap must pass: %v", err)
	}
}

func TestRequireUnmeteredGuardMessageNamesTheFallback(t *testing.T) {
	unmetered := InterfaceMeteringPolicy{AssetMode: meteringAssetFree, PackageMode: meteringSkip, GroupMode: meteringSkip}
	err := requireUnmeteredGuard(unmetered, InterfaceQuotaPolicy{})
	if err == nil || !strings.Contains(err.Error(), "全平台总量") {
		t.Fatalf("error must tell the admin which fallback to set: %v", err)
	}
}
