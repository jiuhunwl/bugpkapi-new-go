package packagepkg

import (
	"strings"
	"testing"
)

func TestPackageVisibilityClauseUsesVisibleRolesWithPurchaseFallback(t *testing.T) {
	viewer := &PackageViewer{RoleIDs: []int64{5, 9}}
	clause, args := packageVisibilityClause("GLOBAL", "bp_package_global.id", "", viewer)
	if !strings.Contains(clause, "bp_package_visible_role") {
		t.Fatalf("clause should consult visible roles: %s", clause)
	}
	// 没有配置可见组的套餐仍按购买组判断，历史行为不能变。
	if !strings.Contains(clause, "not exists") || !strings.Contains(clause, "bp_package_role") {
		t.Fatalf("clause should fall back to purchase roles: %s", clause)
	}
	if want := len(viewer.RoleIDs)*2 + 3; len(args) != want {
		t.Fatalf("args = %d, want %d (%v)", len(args), want, args)
	}
}

func TestPackageVisibilityClauseWithoutRolesOnlyShowsOpenPackages(t *testing.T) {
	clause, args := packageVisibilityClause("GLOBAL", "bp_package_global.id", "", &PackageViewer{})
	if len(args) != 0 || !strings.Contains(clause, "role_restricted=0") {
		t.Fatalf("empty viewer clause = %q args=%v", clause, args)
	}
	if clause, _ := packageVisibilityClause("GLOBAL", "bp_package_global.id", "", nil); clause != "" {
		t.Fatalf("admin viewer should not be filtered, got %q", clause)
	}
}
