package rbac

import (
	"context"
	"strings"
	"testing"
)

func TestHasPermissionAdminBypassesAssignedPermissionRows(t *testing.T) {
	service := NewService(nil, nil)
	allowed, err := service.HasPermission(
		context.Background(),
		1,
		[]string{"admin"},
		"system:any-future-admin-feature",
	)
	if err != nil {
		t.Fatalf("HasPermission() error = %v", err)
	}
	if !allowed {
		t.Fatal("administrator did not receive implicit full permission")
	}
}

func TestValidateMenuParent(t *testing.T) {
	menus := []Menu{
		{ID: 1, PID: 0},
		{ID: 2, PID: 1},
		{ID: 3, PID: 2},
	}

	tests := []struct {
		name     string
		menuID   int64
		parentID int64
		menus    []Menu
		wantErr  bool
	}{
		{
			name:     "root parent allowed",
			menuID:   1,
			parentID: 0,
			menus:    menus,
		},
		{
			name:     "normal parent allowed",
			menuID:   3,
			parentID: 1,
			menus:    menus,
		},
		{
			name:     "missing parent rejected",
			menuID:   1,
			parentID: 99,
			menus:    menus,
			wantErr:  true,
		},
		{
			name:     "direct child parent rejected",
			menuID:   1,
			parentID: 2,
			menus:    menus,
			wantErr:  true,
		},
		{
			name:     "descendant parent rejected",
			menuID:   1,
			parentID: 3,
			menus:    menus,
			wantErr:  true,
		},
		{
			name:     "existing parent cycle rejected",
			menuID:   4,
			parentID: 2,
			menus: []Menu{
				{ID: 1, PID: 3},
				{ID: 2, PID: 1},
				{ID: 3, PID: 2},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateMenuParent(tt.menuID, tt.parentID, tt.menus)
			if tt.wantErr && err == nil {
				t.Fatal("expected error")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestNormalizeMenuRejectsUnsafeRoutesAndNormalizesRoles(t *testing.T) {
	valid, err := normalizeMenu(SaveMenuRequest{
		AuthCode:  "system:test:view",
		Component: "/system/test/index",
		Meta:      map[string]any{"title": "Test"},
		Name:      "TestMenu",
		Path:      "/system/test",
		PID:       float64(0),
		RoleIDs:   []int64{1, 1, 2},
		Type:      "menu",
	}, 0)
	if err != nil {
		t.Fatalf("normalizeMenu() error = %v", err)
	}
	if len(valid.RoleIDs) != 2 {
		t.Fatalf("normalized role ids = %#v", valid.RoleIDs)
	}

	invalid := []SaveMenuRequest{
		{Name: "UnsafePath", Path: "//evil.example", Type: "menu"},
		{Name: "UnsafeLink", Path: "/unsafe-link", LinkSrc: "javascript:alert(1)", Type: "link"},
		{Name: "FractionalParent", Path: "/fractional", PID: 1.5, Type: "menu"},
		{Name: "UnsafeComponent", Path: "/component", Component: "/../secret", Type: "menu"},
		{Name: "InvalidRole", Path: "/role", RoleIDs: []int64{0, 1}, Type: "menu"},
		{Name: "HugeMeta", Path: "/meta", Meta: map[string]any{"value": strings.Repeat("x", maxMenuMetaBytes)}, Type: "menu"},
	}
	for index, request := range invalid {
		if _, err := normalizeMenu(request, 0); err == nil {
			t.Fatalf("invalid menu %d was accepted", index)
		}
	}
}

func TestNormalizeRoleDeduplicatesPermissionsAndRejectsInvalidIDs(t *testing.T) {
	role, err := normalizeRole(SaveRoleRequest{Name: "Operator", RoleKey: "operator", Permissions: []int64{1, 1, 2}})
	if err != nil {
		t.Fatalf("normalizeRole() error = %v", err)
	}
	if len(role.Permissions) != 2 {
		t.Fatalf("normalized permissions = %#v", role.Permissions)
	}
	if _, err := normalizeRole(SaveRoleRequest{Name: "Invalid", RoleKey: "invalid", Permissions: []int64{-1}}); err == nil {
		t.Fatal("normalizeRole accepted a negative permission id")
	}
	invalidQPS := 0
	if _, err := normalizeRole(SaveRoleRequest{Name: "Invalid QPS", RoleKey: "invalid_qps", QPSLimit: &invalidQPS}); err == nil {
		t.Fatal("normalizeRole accepted a zero QPS limit")
	}
	selfRegister := 1
	if _, err := normalizeRole(SaveRoleRequest{Name: "Admin", RoleKey: "admin", SelfRegisterEnabled: &selfRegister}); err == nil {
		t.Fatal("normalizeRole allowed public registration for admin")
	}
}

func TestFilterDuplicateWorkspaceMenus(t *testing.T) {
	menus := []Menu{
		{ID: 1, Name: "Workspace", Path: "/console/workspace"},
		{ID: 90, Name: "DashboardStats", Path: "/dashboard-stats"},
		{ID: 92, Name: "UserDashboardStats", Path: "/dashboard-stats/user"},
		{ID: 93, Name: "LegacyUserDashboard", Path: "/dashboard-stats/user"},
		{ID: 141, Name: "HomeOverview", Path: "/dashboard/home-overview"},
		{ID: 142, Name: "LegacyHomeOverview", Path: "/dashboard/home-overview"},
	}

	filtered := filterDuplicateWorkspaceMenus(menus)
	if len(filtered) != 2 {
		t.Fatalf("filtered menu count = %d, want 2", len(filtered))
	}
	if filtered[0].Name != "Workspace" || filtered[1].Name != "DashboardStats" {
		t.Fatalf("filtered menus = %#v", filtered)
	}
}

func TestFilterMenusForAudience(t *testing.T) {
	menus := []Menu{
		{
			ID:   1,
			Name: "Shared",
			Meta: map[string]any{
				"adminOrder": 2,
				"adminTitle": "navigation.admin.shared",
				"order":      99,
				"title":      "navigation.shared",
				"userOrder":  4,
				"userTitle":  "navigation.user.shared",
			},
		},
		{ID: 2, Name: "UserOnly", Meta: map[string]any{"audience": "user"}},
		{ID: 3, Name: "AdminOnly", Meta: map[string]any{"audience": "admin"}},
	}

	userMenus := filterMenusForAudience(menus, false)
	if len(userMenus) != 2 || userMenus[1].Name != "UserOnly" {
		t.Fatalf("user menus = %#v", userMenus)
	}
	if got := userMenus[0].Meta["title"]; got != "navigation.user.shared" {
		t.Fatalf("user title = %#v", got)
	}
	if got := userMenus[0].Meta["order"]; got != 4 {
		t.Fatalf("user order = %#v", got)
	}

	adminMenus := filterMenusForAudience(menus, true)
	if len(adminMenus) != 2 || adminMenus[1].Name != "AdminOnly" {
		t.Fatalf("admin menus = %#v", adminMenus)
	}
	if got := adminMenus[0].Meta["title"]; got != "navigation.admin.shared" {
		t.Fatalf("admin title = %#v", got)
	}
	if got := adminMenus[0].Meta["order"]; got != 2 {
		t.Fatalf("admin order = %#v", got)
	}

	if got := menus[0].Meta["title"]; got != "navigation.shared" {
		t.Fatalf("source menu metadata was mutated: %#v", menus[0].Meta)
	}
}

func TestCollapseKeyMenuFlattensLegacyListForEachAudience(t *testing.T) {
	menus := []Menu{
		{ID: 30, PID: 0, Name: "ApiKeyManagement", Path: "/key", Type: "catalog", Meta: map[string]any{"title": "密钥管理"}},
		{ID: 31, PID: 30, Name: "ApiKeyList", Path: "/key/list", Component: "/key/list", Type: "menu", Meta: map[string]any{"title": "密钥列表"}},
	}

	adminMenus := collapseKeyMenu(menus, false)
	if len(adminMenus) != 1 || adminMenus[0].Path != "/key" || adminMenus[0].Component != "/key/list" {
		t.Fatalf("admin key menus = %#v", adminMenus)
	}
	if got := adminMenus[0].Meta["title"]; got != "system.key.title" {
		t.Fatalf("admin key title = %#v", got)
	}

	userMenus := collapseKeyMenu(menus, true)
	if len(userMenus) != 1 || userMenus[0].PID != 0 {
		t.Fatalf("user key menus = %#v", userMenus)
	}
	if got := userMenus[0].Meta["title"]; got != "system.key.myKeys" {
		t.Fatalf("user key title = %#v", got)
	}
}

func TestCollapseKeyMenuPromotesOrphanedLegacyList(t *testing.T) {
	menus := collapseKeyMenu([]Menu{{
		ID: 31, PID: 30, Name: "ApiKeyList", Path: "/key/list", Component: "/key/list", Type: "menu",
	}}, true)
	if len(menus) != 1 || menus[0].Path != "/key" || menus[0].PID != 0 {
		t.Fatalf("promoted key menus = %#v", menus)
	}
}

func TestInjectUserFeatureMenusAddsDirectSelfServiceRoutes(t *testing.T) {
	menus := injectUserFeatureMenus(nil)
	want := map[string]bool{
		"/console/call-logs":  false,
		"/console/interfaces": false,
		"/friend-link/mine":   false,
		"/key":                false,
	}
	for _, menu := range menus {
		if _, ok := want[menu.Path]; ok {
			want[menu.Path] = true
			if menu.PID != 0 || menu.Component == "" {
				t.Fatalf("injected menu is not a direct route: %#v", menu)
			}
		}
	}
	for path, present := range want {
		if !present {
			t.Fatalf("missing injected route %s: %#v", path, menus)
		}
	}
}

func TestNormalizePermissionCodesAddsFlattenedKeyListPermission(t *testing.T) {
	codes := normalizePermissionCodes([]string{"key:root", "key:create"})
	if !containsString(codes, "key:list") {
		t.Fatalf("flattened key menu did not provide key:list: %#v", codes)
	}

	codes = normalizePermissionCodes([]string{"key:list"})
	if countString(codes, "key:list") != 1 {
		t.Fatalf("key:list was duplicated: %#v", codes)
	}
}

func TestUserSelfServicePermissionsDoNotSelectAdminEndpoints(t *testing.T) {
	codes := normalizePermissionCodes([]string{
		"key:create",
		"key:delete",
		"key:interface",
		"key:ip-whitelist",
		"key:list",
		"key:status",
		"key:update",
		"system:about:view",
	})
	if !containsString(codes, "key:list") {
		t.Fatalf("user permissions are missing key:list: %#v", codes)
	}
	if containsString(codes, "interface:list") || containsString(codes, "log:call:list") {
		t.Fatalf("user permissions selected administrator endpoints: %#v", codes)
	}
}

func TestUserMenuOutputInjectsScopedRoutesWithoutManagementCodes(t *testing.T) {
	// This models the post-000077 ordinary-user database result: key actions
	// and About are assigned, while interface:list/log:call:list remain admin
	// permissions. Route visibility is provided independently by the injector.
	allowedMenus := []Menu{
		{ID: 60, PID: 0, Name: "ApiKeyManagement", Path: "/key", Component: "/key/list", Type: "menu", AuthCode: "key:list", Meta: map[string]any{"order": 3}},
		{ID: 178, PID: 0, Name: "SystemAbout", Path: "/system/about", Component: "/system/about/index", Type: "menu", AuthCode: "system:about:view", Meta: map[string]any{"order": 8}},
	}
	output := normalizeConsoleFeatureMenus(allowedMenus)
	output = collapseKeyMenu(output, true)
	output = injectUserFeatureMenus(output)
	output = normalizeUserNavigationMenus(output)
	tree := BuildTree(output, false)

	for _, path := range []string{"/console/interfaces", "/console/call-logs", "/friend-link/mine", "/key"} {
		if countMenuPath(tree, path) != 1 {
			t.Fatalf("scoped user route %s output = %#v", path, tree)
		}
	}
	if countMenuAuthCode(tree, "interface:list") != 0 || countMenuAuthCode(tree, "log:call:list") != 0 {
		t.Fatalf("injected user routes exposed management codes: %#v", tree)
	}
	if countMenuAuthCode(tree, "key:list") != 1 {
		t.Fatalf("user key:list permission missing from route output: %#v", tree)
	}
}

func TestNormalizeConsoleFeatureMenusRemovesLegacyAndDuplicateRoutes(t *testing.T) {
	menus := normalizeConsoleFeatureMenus([]Menu{
		{ID: 1, Name: "LegacyConsoleAPIs", Path: "/console/apis"},
		{ID: 2, Name: "InterfaceList", Path: "/interface/list"},
		{ID: 3, Name: "InterfaceListCopy", Path: "/console/interfaces"},
		{ID: 4, Name: "InterfaceCallLog", Path: "/interface/log"},
	})

	if len(menus) != 2 {
		t.Fatalf("normalized console menus = %#v", menus)
	}
	if menus[0].Path != "/console/interfaces" || menus[1].Path != "/console/call-logs" {
		t.Fatalf("normalized paths = %#v", menus)
	}
}

func TestNormalizeUserNavigationMenusRemovesManagementCatalog(t *testing.T) {
	menus := normalizeUserNavigationMenus([]Menu{
		{ID: 40, PID: 0, Name: "InterfaceManagement", Path: "/interface", Type: "catalog"},
		{ID: 41, PID: 40, Name: "InterfaceList", Path: "/console/interfaces", Type: "menu"},
		{ID: 50, PID: 40, Name: "InterfaceCallLog", Path: "/console/call-logs", Type: "menu"},
		{ID: 178, PID: 30, Name: "SystemAbout", Path: "/system/about", Type: "menu", Meta: map[string]any{"order": 8}},
	})
	if len(menus) != 3 || menus[0].PID != 0 || menus[1].PID != 0 {
		t.Fatalf("user navigation = %#v", menus)
	}
	if menus[2].PID != 0 || menus[2].Meta["order"] != lastNavigationOrder {
		t.Fatalf("user about navigation = %#v", menus[2])
	}
}

func TestNormalizeAdminNavigationMenusMovesAboutToBottom(t *testing.T) {
	menus := normalizeAdminNavigationMenus([]Menu{{
		ID: 178, PID: 30, Name: "SystemAbout", Path: "/system/about", Meta: map[string]any{"order": 8},
	}})
	if menus[0].PID != 0 || menus[0].Meta["order"] != lastNavigationOrder {
		t.Fatalf("about navigation = %#v", menus[0])
	}
}

func TestNavigationRouteOutputKeepsOneCanonicalSelfServiceEntry(t *testing.T) {
	source := []Menu{
		{ID: 40, PID: 0, Name: "InterfaceManagement", Path: "/interface", Type: "catalog", Meta: map[string]any{"order": 2}},
		{ID: 41, PID: 40, Name: "InterfaceList", Path: "/interface/list", Component: "/interface/list", Type: "menu", Meta: map[string]any{"order": 1}},
		{ID: 50, PID: 40, Name: "InterfaceCallLog", Path: "/interface/log", Component: "/interface/log", Type: "menu", Meta: map[string]any{"order": 2}},
		{ID: 60, PID: 0, Name: "ApiKeyManagement", Path: "/key", Component: "/key/list", Type: "menu", Meta: map[string]any{"order": 3}},
		{ID: 178, PID: 30, Name: "SystemAbout", Path: "/system/about", Component: "/system/about/index", Type: "menu", Meta: map[string]any{"order": 8}},
		{ID: 301, PID: 0, Name: "LegacyConsoleAPIs", Path: "/console/apis", Type: "menu"},
		{ID: 302, PID: 0, Name: "DuplicateInterfaceList", Path: "/console/interfaces", Type: "menu"},
	}

	userMenus := normalizeConsoleFeatureMenus(source)
	userMenus = collapseKeyMenu(userMenus, true)
	userMenus = injectUserFeatureMenus(userMenus)
	userMenus = normalizeUserNavigationMenus(userMenus)
	userTree := BuildTree(userMenus, false)
	if countMenuPath(userTree, "/console/interfaces") != 1 {
		t.Fatalf("user interface route output = %#v", userTree)
	}
	if countMenuPath(userTree, "/console/call-logs") != 1 {
		t.Fatalf("user call-log route output = %#v", userTree)
	}
	if countMenuPath(userTree, "/key") != 1 {
		t.Fatalf("user key route output = %#v", userTree)
	}
	if countMenuPath(userTree, "/console/apis") != 0 {
		t.Fatalf("legacy admin interface route leaked to user output = %#v", userTree)
	}
	if len(userTree) == 0 || userTree[len(userTree)-1].Path != "/system/about" {
		t.Fatalf("about route is not last in user output = %#v", userTree)
	}

	adminMenus := normalizeConsoleFeatureMenus(source)
	adminMenus = collapseKeyMenu(adminMenus, false)
	adminMenus = normalizeAdminNavigationMenus(adminMenus)
	adminTree := BuildTree(adminMenus, false)
	if countMenuPath(adminTree, "/console/interfaces") != 1 || countMenuPath(adminTree, "/console/apis") != 0 {
		t.Fatalf("admin interface route output = %#v", adminTree)
	}
	if len(adminTree) == 0 || adminTree[len(adminTree)-1].Path != "/system/about" {
		t.Fatalf("about route is not last in admin output = %#v", adminTree)
	}
}

func countMenuPath(menus []Menu, path string) int {
	count := 0
	for _, menu := range menus {
		if menu.Path == path {
			count++
		}
		count += countMenuPath(menu.Children, path)
	}
	return count
}

func countMenuAuthCode(menus []Menu, authCode string) int {
	count := 0
	for _, menu := range menus {
		if menu.AuthCode == authCode {
			count++
		}
		count += countMenuAuthCode(menu.Children, authCode)
	}
	return count
}

func containsString(items []string, expected string) bool {
	return countString(items, expected) > 0
}

func countString(items []string, expected string) int {
	count := 0
	for _, item := range items {
		if item == expected {
			count++
		}
	}
	return count
}

func TestApplyEffectiveRolePermissionsUsesSuperAdminPolicy(t *testing.T) {
	roles := []Role{
		{RoleKey: "admin", Permissions: []int64{99}},
		{RoleKey: "user", Permissions: []int64{7}},
	}
	menus := []Menu{
		{ID: 3, Status: 1, Meta: map[string]any{"audience": "admin"}},
		{ID: 1, Status: 1, Meta: map[string]any{"audience": "all"}},
		{ID: 2, Status: 1, Meta: map[string]any{"audience": "user"}},
	}

	applyEffectiveRolePermissions(roles, menus)

	if roles[0].PermissionMode != "all" {
		t.Fatalf("admin permission mode = %q", roles[0].PermissionMode)
	}
	if got := roles[0].EffectivePermissions; len(got) != 3 || got[0] != 1 || got[1] != 2 || got[2] != 3 {
		t.Fatalf("admin effective permissions = %v, want [1 2 3]", got)
	}
	if got := roles[0].Permissions; len(got) != 1 || got[0] != 99 {
		t.Fatalf("admin assigned permissions were overwritten: %v", got)
	}
	if roles[1].PermissionMode != "assigned" || len(roles[1].EffectivePermissions) != 1 || roles[1].EffectivePermissions[0] != 7 {
		t.Fatalf("regular role effective permissions = %#v", roles[1])
	}
}
