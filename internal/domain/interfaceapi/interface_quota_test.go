package interfaceapi

import "testing"

func TestNormalizeInterfaceQuotaAcceptsDefaultsAndLadder(t *testing.T) {
	policy, err := normalizeInterfaceQuota(InterfaceQuotaPolicy{
		Enabled:       1,
		CountMode:     "success",
		PerUserHour:   10,
		PerUserDay:    100,
		PerUserWeek:   500,
		PerUserMonth:  1000,
		AllUsersHour:  1000,
		AllUsersDay:   10000,
		AllUsersWeek:  50000,
		AllUsersMonth: 100000,
	})
	if err != nil {
		t.Fatalf("normalizeInterfaceQuota() error = %v", err)
	}
	if policy.CountMode != "success" || policy.PerUserDay != 100 || policy.AllUsersMonth != 100000 {
		t.Fatalf("normalized policy = %+v", policy)
	}
}

func TestNormalizeInterfaceQuotaRejectsBrokenLadderAndUserAboveAllUsers(t *testing.T) {
	tests := []InterfaceQuotaPolicy{
		{PerUserHour: 100, PerUserDay: 10},
		{PerUserDay: 100, PerUserWeek: 10},
		{PerUserWeek: 100, PerUserMonth: 10},
		{AllUsersHour: 100, AllUsersDay: 10},
		{PerUserHour: 100, AllUsersHour: 10},
		{Enabled: 2},
		{CountMode: "sometimes"},
	}
	for index, req := range tests {
		if _, err := normalizeInterfaceQuota(req); err == nil {
			t.Fatalf("invalid interface quota %d was accepted", index)
		}
	}
}

func TestNormalizeInterfaceQuotaRoleOverrides(t *testing.T) {
	policy, err := normalizeInterfaceQuota(InterfaceQuotaPolicy{
		Enabled: 1,
		RoleOverrides: []InterfaceRoleQuota{
			{RoleID: 2, Mode: "INHERIT"},
			{RoleID: 3, Mode: "unlimited"},
			{RoleID: 4, Mode: "adjust", AdjustHour: 10, AdjustDay: -20},
			{RoleID: 5, Mode: "override", OverrideHour: 1, OverrideDay: 2},
		},
	})
	if err != nil {
		t.Fatalf("normalizeInterfaceQuota() error = %v", err)
	}
	if len(policy.RoleOverrides) != 3 {
		t.Fatalf("inherit row must be dropped, got %+v", policy.RoleOverrides)
	}
	if policy.RoleOverrides[0].Mode != "unlimited" || policy.RoleOverrides[1].Mode != "adjust" {
		t.Fatalf("override modes = %+v", policy.RoleOverrides)
	}
}

func TestNormalizeInterfaceQuotaAcceptsIgnoreRoleMode(t *testing.T) {
	policy, err := normalizeInterfaceQuota(InterfaceQuotaPolicy{
		Enabled: 1,
		RoleOverrides: []InterfaceRoleQuota{
			{RoleID: 6, Mode: "IGNORE"},
			{RoleID: 7, Mode: "unlimited"},
		},
	})
	if err != nil {
		t.Fatalf("normalizeInterfaceQuota() error = %v", err)
	}
	if len(policy.RoleOverrides) != 2 || policy.RoleOverrides[0].Mode != "ignore" {
		t.Fatalf("ignore override must be normalised, got %+v", policy.RoleOverrides)
	}
}

func TestNormalizeInterfaceQuotaRejectsDuplicateAndUnknownRoleModes(t *testing.T) {
	for _, req := range []InterfaceQuotaPolicy{
		{RoleOverrides: []InterfaceRoleQuota{{RoleID: 2, Mode: "adjust"}, {RoleID: 2, Mode: "unlimited"}}},
		{RoleOverrides: []InterfaceRoleQuota{{RoleID: 2, Mode: "bogus"}}},
		{RoleOverrides: []InterfaceRoleQuota{{RoleID: 0, Mode: "unlimited"}}},
		{RoleOverrides: []InterfaceRoleQuota{{RoleID: 2, Mode: "override", OverrideHour: -1}}},
	} {
		if _, err := normalizeInterfaceQuota(req); err == nil {
			t.Fatalf("invalid role override was accepted: %+v", req)
		}
	}
}
