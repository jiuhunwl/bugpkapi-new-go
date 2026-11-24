package interfaceapi

import (
	"testing"
)

func TestNormalizeInterfaceDegradeNormalizesWindowAndInheritsDefaultTarget(t *testing.T) {
	policy, err := normalizeInterfaceDegrade(InterfaceDegradePolicy{
		Enabled:                  1,
		DefaultTargetInterfaceID: 20,
		RoleOverrides: []InterfaceRoleDegrade{
			{RoleID: 2, Window: "DAY", Threshold: 5},
			{RoleID: 1, Window: "month", Threshold: 9, TargetInterfaceID: 20},
			{RoleID: 3, Window: "total", Threshold: 0},
		},
	})
	if err != nil {
		t.Fatalf("normalize failed: %v", err)
	}
	if len(policy.RoleOverrides) != 2 {
		t.Fatalf("role overrides = %d, want 2 (threshold 0 row must be dropped)", len(policy.RoleOverrides))
	}
	if policy.RoleOverrides[0].RoleID != 1 || policy.RoleOverrides[0].Window != "month" ||
		policy.RoleOverrides[0].TargetInterfaceID != 0 {
		t.Fatalf("unexpected first role override: %+v", policy.RoleOverrides[0])
	}
	if policy.RoleOverrides[1].RoleID != 2 || policy.RoleOverrides[1].Window != "day" {
		t.Fatalf("unexpected second role override: %+v", policy.RoleOverrides[1])
	}
}

func TestNormalizeInterfaceDegradeSupportsCustomTargetURL(t *testing.T) {
	policy, err := normalizeInterfaceDegrade(InterfaceDegradePolicy{
		Enabled:          1,
		DefaultTargetURL: "http://10.0.0.8:8080/internal/{text}",
		RoleOverrides: []InterfaceRoleDegrade{
			{RoleID: 1, Window: "day", Threshold: 5},
			{RoleID: 2, Window: "day", Threshold: 6, TargetURL: " http://10.0.0.9/internal "},
			{RoleID: 3, Window: "day", Threshold: 7, TargetURL: "http://10.0.0.8:8080/internal/{text}"},
		},
	})
	if err != nil {
		t.Fatalf("normalize failed: %v", err)
	}
	if policy.DefaultTargetURL != "http://10.0.0.8:8080/internal/{text}" {
		t.Fatalf("default target url = %q", policy.DefaultTargetURL)
	}
	byRole := map[int64]InterfaceRoleDegrade{}
	for _, item := range policy.RoleOverrides {
		byRole[item.RoleID] = item
	}
	if byRole[2].TargetURL != "http://10.0.0.9/internal" {
		t.Fatalf("role 2 target url = %q", byRole[2].TargetURL)
	}
	if byRole[3].TargetURL != "" {
		t.Fatalf("role target matching default should inherit, got %q", byRole[3].TargetURL)
	}
}

func TestNormalizeInterfaceDegradeRejectsAmbiguousCustomTarget(t *testing.T) {
	cases := []InterfaceDegradePolicy{
		{
			Enabled:                  1,
			DefaultTargetInterfaceID: 20,
			DefaultTargetURL:         "http://10.0.0.8/internal",
			RoleOverrides:            []InterfaceRoleDegrade{{RoleID: 1, Window: "day", Threshold: 5}},
		},
		{
			Enabled:          1,
			DefaultTargetURL: "not-a-url",
			RoleOverrides:    []InterfaceRoleDegrade{{RoleID: 1, Window: "day", Threshold: 5}},
		},
		{
			Enabled:                  1,
			DefaultTargetInterfaceID: 20,
			RoleOverrides: []InterfaceRoleDegrade{{
				RoleID: 1, Window: "day", Threshold: 5,
				TargetInterfaceID: 21, TargetURL: "http://10.0.0.9/internal",
			}},
		},
	}
	for index, policy := range cases {
		if _, err := normalizeInterfaceDegrade(policy); err == nil {
			t.Fatalf("case %d: expected validation error", index)
		}
	}
}

func TestNormalizeInterfaceDegradeRejectsInvalidConfigurations(t *testing.T) {
	cases := []struct {
		name   string
		policy InterfaceDegradePolicy
	}{
		{
			name: "enabled without default target",
			policy: InterfaceDegradePolicy{
				Enabled:       1,
				RoleOverrides: []InterfaceRoleDegrade{{RoleID: 1, Threshold: 5, Window: "day"}},
			},
		},
		{
			name: "enabled without roles",
			policy: InterfaceDegradePolicy{
				Enabled:                  1,
				DefaultTargetInterfaceID: 20,
			},
		},
		{
			name: "duplicate role",
			policy: InterfaceDegradePolicy{
				Enabled:                  1,
				DefaultTargetInterfaceID: 20,
				RoleOverrides: []InterfaceRoleDegrade{
					{RoleID: 1, Threshold: 5, Window: "day"},
					{RoleID: 1, Threshold: 6, Window: "day"},
				},
			},
		},
		{
			name: "invalid window",
			policy: InterfaceDegradePolicy{
				Enabled:                  1,
				DefaultTargetInterfaceID: 20,
				RoleOverrides:            []InterfaceRoleDegrade{{RoleID: 1, Threshold: 5, Window: "hour"}},
			},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := normalizeInterfaceDegrade(testCase.policy); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestSameInterfaceDegradePolicyIgnoresDisplayFields(t *testing.T) {
	left := InterfaceDegradePolicy{
		Enabled:                  1,
		DefaultTargetInterfaceID: 20,
		RoleOverrides: []InterfaceRoleDegrade{
			{RoleID: 1, Threshold: 5, Window: "day"},
			{RoleID: 2, Threshold: 7, Window: "month", TargetInterfaceID: 30},
		},
	}
	right := InterfaceDegradePolicy{
		Enabled:                    1,
		DefaultTargetInterfaceID:   20,
		DefaultTargetInterfaceName: "target",
		PolicyVersion:              99,
		RoleOverrides: []InterfaceRoleDegrade{
			{RoleID: 2, Threshold: 7, Window: "month", TargetInterfaceID: 30, RoleName: "group"},
			{RoleID: 1, Threshold: 5, Window: "day"},
		},
	}
	if !sameInterfaceDegradePolicy(left, right) {
		t.Fatal("display-only changes must not reset counters")
	}
	right.RoleOverrides[0].Threshold = 8
	if sameInterfaceDegradePolicy(left, right) {
		t.Fatal("threshold change must reset counters")
	}
}
