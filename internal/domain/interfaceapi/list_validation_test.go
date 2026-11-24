package interfaceapi

import (
	"strings"
	"testing"
)

func TestNormalizeInterfaceListParamsRejectsUnsafeFilters(t *testing.T) {
	invalid := 2
	for name, params := range map[string]ListParams{
		"featured":       {IsFeatured: &invalid, Page: 1, PageSize: 20},
		"keyword":        {Keyword: strings.Repeat("查", 129), Page: 1, PageSize: 20},
		"method":         {Page: 1, PageSize: 20, RequestMethod: "DELETE"},
		"price type":     {Page: 1, PageSize: 20, PriceType: "UNKNOWN"},
		"page":           {Page: 0, PageSize: 20},
		"page size":      {Page: 1, PageSize: 101},
		"public feature": {FeaturedOnly: &invalid, Page: 1, PageSize: 20},
	} {
		t.Run(name, func(t *testing.T) {
			if err := normalizeListParams(&params); err == nil {
				t.Fatal("unsafe interface-list filter was accepted")
			}
		})
	}
}
