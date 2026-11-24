package gateway

import (
	"strings"
	"testing"
)

func TestNormalizeKeyListParamsRejectsUnsafeFilters(t *testing.T) {
	invalid := 2
	for name, params := range map[string]KeyListParams{
		"keyword":  {Keyword: strings.Repeat("k", 129), Page: 1, PageSize: 20},
		"key type": {KeyType: "UNKNOWN", Page: 1, PageSize: 20},
		"page":     {Page: 1001, PageSize: 20},
		"size":     {Page: 1, PageSize: 101},
		"status":   {Page: 1, PageSize: 20, Status: &invalid},
	} {
		t.Run(name, func(t *testing.T) {
			if err := normalizeKeyListParams(&params); err == nil {
				t.Fatal("unsafe key-list filter was accepted")
			}
		})
	}
}

func TestNormalizeKeyListParamsCanonicalizesOwnerScope(t *testing.T) {
	params := KeyListParams{OwnerScope: " mine ", Page: 1, PageSize: 20}
	if err := normalizeKeyListParams(&params); err != nil {
		t.Fatalf("normalizeKeyListParams() error = %v", err)
	}
	if params.OwnerScope != keyOwnerScopeMine {
		t.Fatalf("OwnerScope = %q, want %q", params.OwnerScope, keyOwnerScopeMine)
	}

	for _, invalid := range []string{"users", "current-user", "1"} {
		params := KeyListParams{OwnerScope: invalid, Page: 1, PageSize: 20}
		if err := normalizeKeyListParams(&params); err == nil {
			t.Fatalf("invalid owner scope %q was accepted", invalid)
		}
	}
}

func TestResolveKeyListOwnerScopeKeepsNonAdminOwnerScoped(t *testing.T) {
	if all, err := resolveKeyListOwnerScope("", false); err != nil || all {
		t.Fatalf("default non-admin scope = all:%v err:%v, want owner-scoped", all, err)
	}
	if all, err := resolveKeyListOwnerScope("mine", true); err != nil || all {
		t.Fatalf("admin mine scope = all:%v err:%v, want owner-scoped", all, err)
	}
	if all, err := resolveKeyListOwnerScope("all", true); err != nil || !all {
		t.Fatalf("admin all scope = all:%v err:%v, want all", all, err)
	}
	if _, err := resolveKeyListOwnerScope("all", false); err == nil {
		t.Fatal("non-admin all scope was accepted")
	}
}
