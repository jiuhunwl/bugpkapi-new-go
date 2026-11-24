package packagepkg

import (
	"testing"
)

func intPtr(value int) *int { return &value }

func TestNormalizeHiddenDefaultsAndValidates(t *testing.T) {
	// nil = 不隐藏（老请求不带 hidden 字段时保持原行为）。
	if got, err := normalizeHidden(nil); err != nil || got != 0 {
		t.Fatalf("normalizeHidden(nil) = (%d, %v), want (0, nil)", got, err)
	}
	if got, err := normalizeHidden(intPtr(1)); err != nil || got != 1 {
		t.Fatalf("normalizeHidden(1) = (%d, %v), want (1, nil)", got, err)
	}
	if _, err := normalizeHidden(intPtr(2)); err == nil {
		t.Fatal("normalizeHidden(2) should reject non-binary value")
	}
}

func TestNormalizeGlobalAppliesHidden(t *testing.T) {
	item, err := normalizeGlobal(SaveGlobalPackageRequest{
		Name:      "内发套餐",
		Price:     "0",
		ValidDays: 30,
		Hidden:    intPtr(1),
	})
	if err != nil {
		t.Fatalf("normalizeGlobal returned error: %v", err)
	}
	if item.Hidden != 1 {
		t.Fatalf("normalizeGlobal lost hidden flag: %d", item.Hidden)
	}
}

func TestNormalizeGrantUsersTrimsDedupesAndLimits(t *testing.T) {
	users, err := normalizeGrantUsers([]string{" alice ", "alice", "42", "", "bob"})
	if err != nil {
		t.Fatalf("normalizeGrantUsers returned error: %v", err)
	}
	if len(users) != 3 || users[0] != "alice" || users[1] != "42" || users[2] != "bob" {
		t.Fatalf("normalizeGrantUsers = %#v, want [alice 42 bob]", users)
	}
	if _, err := normalizeGrantUsers([]string{"   "}); err == nil {
		t.Fatal("normalizeGrantUsers should reject empty list")
	}
	if _, err := normalizeGrantUsers(make([]string, maxGrantUsers+1)); err == nil {
		t.Fatal("normalizeGrantUsers should enforce maxGrantUsers")
	}
}

func TestNormalizeGrantScopeAcceptsPoint(t *testing.T) {
	for raw, want := range map[string]string{
		"global":    "GLOBAL",
		"interface": "INTERFACE",
		"point":     "POINT",
	} {
		got, err := normalizeGrantScope(raw)
		if err != nil || got != want {
			t.Fatalf("normalizeGrantScope(%q) = (%q, %v), want (%q, nil)", raw, got, err, want)
		}
	}
	if _, err := normalizeGrantScope("wallet"); err == nil {
		t.Fatal("normalizeGrantScope should reject unknown scope")
	}
}
