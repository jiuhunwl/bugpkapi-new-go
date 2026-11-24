package packagepkg

import "testing"

func TestNormalizeGlobalPackageLimits(t *testing.T) {
	base := SaveGlobalPackageRequest{
		DailyLimit:   0,
		Name:         "限量套餐",
		Price:        "9.9",
		QPSLimit:     10,
		StockLimit:   100,
		PerUserLimit: 2,
		ValidDays:    30,
	}
	item, err := normalizeGlobal(base)
	if err != nil {
		t.Fatalf("normalizeGlobal() error = %v", err)
	}
	if item.StockLimit != 100 || item.PerUserLimit != 2 {
		t.Fatalf("limits = %d/%d, want 100/2", item.StockLimit, item.PerUserLimit)
	}

	negative := base
	negative.StockLimit = -1
	if _, err := normalizeGlobal(negative); err == nil {
		t.Fatal("negative stock limit was accepted")
	}
}

func TestNormalizeSaleWindowRejectsReversedRange(t *testing.T) {
	cases := []struct {
		name       string
		start, end string
		wantErr    bool
	}{
		{name: "empty", start: "", end: ""},
		{name: "only start", start: "2026-11-01 10:00:00"},
		{name: "ordered", start: "2026-11-01 10:00:00", end: "2026-11-02 10:00:00"},
		{name: "equal", start: "2026-11-01 10:00:00", end: "2026-11-01 10:00:00", wantErr: true},
		{name: "reversed", start: "2026-11-02 10:00:00", end: "2026-11-01 10:00:00", wantErr: true},
		{name: "invalid", start: "not-a-time", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := normalizeSaleWindow(tc.start, tc.end)
			if (err != nil) != tc.wantErr {
				t.Fatalf("normalizeSaleWindow(%q,%q) error = %v, wantErr %v", tc.start, tc.end, err, tc.wantErr)
			}
		})
	}
}

func TestNormalizePackageRoleLimit(t *testing.T) {
	restricted, visibleOnly, roleIDs, err := normalizePackageRoleLimit(1, 1, []int64{3, 2, 3})
	if err != nil {
		t.Fatalf("normalizePackageRoleLimit() error = %v", err)
	}
	if restricted != 1 || visibleOnly != 1 || len(roleIDs) != 2 || roleIDs[0] != 3 || roleIDs[1] != 2 {
		t.Fatalf("role limit = %d/%d/%v, want 1/1/[3 2]", restricted, visibleOnly, roleIDs)
	}

	// 未启用限制时，残留的可见性标记与用户组必须被清空。
	restricted, visibleOnly, roleIDs, err = normalizePackageRoleLimit(0, 1, []int64{3})
	if err != nil || restricted != 0 || visibleOnly != 0 || roleIDs != nil {
		t.Fatalf("disabled role limit = %d/%d/%v err=%v, want 0/0/nil", restricted, visibleOnly, roleIDs, err)
	}

	for name, tc := range map[string]struct {
		restricted, visibleOnly int
		roleIDs                 []int64
	}{
		"enabled without roles": {restricted: 1},
		"invalid flag":          {restricted: 2},
		"invalid visible flag":  {restricted: 1, visibleOnly: -1, roleIDs: []int64{1}},
		"invalid role id":       {restricted: 1, roleIDs: []int64{0}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, _, err := normalizePackageRoleLimit(tc.restricted, tc.visibleOnly, tc.roleIDs); err == nil {
				t.Fatal("invalid role limit was accepted")
			}
		})
	}
}

func TestPackageViewerAllows(t *testing.T) {
	viewer := &PackageViewer{RoleIDs: []int64{2, 5}}
	if !viewer.Allows(0, nil) {
		t.Fatal("unrestricted package should be purchasable")
	}
	if !viewer.Allows(1, []int64{5, 7}) {
		t.Fatal("viewer role missing from allowed list was rejected")
	}
	if viewer.Allows(1, []int64{7, 8}) {
		t.Fatal("viewer outside allowed roles was accepted")
	}
	var adminView *PackageViewer
	if !adminView.Allows(1, []int64{7}) {
		t.Fatal("management view should not be filtered")
	}
}
