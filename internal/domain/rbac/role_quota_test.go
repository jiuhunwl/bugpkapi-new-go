package rbac

import "testing"

func int64Ptr(value int64) *int64 { return &value }

func TestNormalizeRoleQuotaAcceptsLadder(t *testing.T) {
	minute, hour, day, month := int64Ptr(30), int64Ptr(300), int64Ptr(2000), int64Ptr(20000)
	mode := "success"
	enabled := 1
	role, err := normalizeRole(SaveRoleRequest{
		Name:             "Standard",
		RoleKey:          "standard",
		QuotaEnabled:     &enabled,
		QuotaMinuteLimit: minute,
		QuotaHourLimit:   hour,
		QuotaDayLimit:    day,
		QuotaMonthLimit:  month,
		QuotaCountMode:   &mode,
	})
	if err != nil {
		t.Fatalf("normalizeRole() error = %v", err)
	}
	if role.QuotaEnabled != 1 || role.QuotaMinuteLimit != 30 || role.QuotaHourLimit != 300 ||
		role.QuotaDayLimit != 2000 || role.QuotaMonthLimit != 20000 || role.QuotaCountMode != "success" {
		t.Fatalf("normalized quota = %+v", role)
	}
}

func TestNormalizeRoleQuotaPreservesDefaultsOnPartialUpdate(t *testing.T) {
	defaults := RoleQuota{
		Enabled:     0,
		MinuteLimit: 10,
		HourLimit:   100,
		DayLimit:    1000,
		MonthLimit:  10000,
		CountMode:   "success",
	}
	enabled := 1
	role, err := normalizeRoleWithDefaults(SaveRoleRequest{
		Name:          "Partial",
		RoleKey:       "partial",
		QuotaEnabled:  &enabled,
		QuotaDayLimit: int64Ptr(500),
	}, defaults)
	if err != nil {
		t.Fatalf("normalizeRoleWithDefaults() error = %v", err)
	}
	if role.QuotaEnabled != 1 || role.QuotaMinuteLimit != 10 || role.QuotaHourLimit != 100 ||
		role.QuotaDayLimit != 500 || role.QuotaMonthLimit != 10000 || role.QuotaCountMode != "success" {
		t.Fatalf("partial quota merge = %+v", role)
	}
}

func TestNormalizeRoleQuotaRejectsBrokenLaddersAndModes(t *testing.T) {
	tests := []SaveRoleRequest{
		{Name: "A", RoleKey: "a", QuotaMinuteLimit: int64Ptr(100), QuotaHourLimit: int64Ptr(10)},
		{Name: "B", RoleKey: "b", QuotaHourLimit: int64Ptr(100), QuotaDayLimit: int64Ptr(10)},
		{Name: "C", RoleKey: "c", QuotaDayLimit: int64Ptr(100), QuotaMonthLimit: int64Ptr(10)},
		{Name: "D", RoleKey: "d", QuotaCountMode: stringPtr("sometimes")},
		{Name: "E", RoleKey: "e", QuotaMinuteLimit: int64Ptr(-1)},
	}
	for index, request := range tests {
		if _, err := normalizeRole(request); err == nil {
			t.Fatalf("invalid quota request %d was accepted", index)
		}
	}
}

func TestNormalizeRoleQuotaAllowsExplicitUnlimited(t *testing.T) {
	role, err := normalizeRole(SaveRoleRequest{
		Name:             "Enterprise",
		RoleKey:          "enterprise",
		QuotaMinuteLimit: int64Ptr(0),
		QuotaHourLimit:   int64Ptr(0),
		QuotaDayLimit:    int64Ptr(0),
		QuotaMonthLimit:  int64Ptr(0),
	})
	if err != nil {
		t.Fatalf("normalizeRole() error = %v", err)
	}
	if role.QuotaMinuteLimit != 0 || role.QuotaMonthLimit != 0 {
		t.Fatalf("explicit unlimited quota was rewritten: %+v", role)
	}
}

func stringPtr(value string) *string { return &value }
