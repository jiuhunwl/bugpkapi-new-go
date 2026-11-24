package user

import (
	"strings"
	"testing"
)

func TestNormalizeUserListParamsRejectsUnsafeFilters(t *testing.T) {
	invalid := 2
	for name, params := range map[string]UserListParams{
		"keyword": {Keyword: strings.Repeat("用", 129), Page: 1, PageSize: 20},
		"page":    {Page: 0, PageSize: 20},
		"role":    {Page: 1, PageSize: 20, RoleID: -1},
		"size":    {Page: 1, PageSize: 201},
		"status":  {Page: 1, PageSize: 20, Status: &invalid},
	} {
		t.Run(name, func(t *testing.T) {
			if err := normalizeUserListParams(&params); err == nil {
				t.Fatal("unsafe user-list filter was accepted")
			}
		})
	}
}
