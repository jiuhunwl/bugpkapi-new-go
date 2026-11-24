package packagepkg

import (
	"strings"
	"testing"
)

func TestValidatePackageListParamsRejectsUnsafeFilters(t *testing.T) {
	invalid := 2
	for name, params := range map[string]ListParams{
		"interface": {InterfaceID: -1, Page: 1, PageSize: 20},
		"keyword":   {Keyword: strings.Repeat("套", 129), Page: 1, PageSize: 20},
		"page":      {Page: 0, PageSize: 20},
		"size":      {Page: 1, PageSize: 101},
		"status":    {Page: 1, PageSize: 20, Status: &invalid},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateListParams(&params); err == nil {
				t.Fatal("unsafe package-list filter was accepted")
			}
		})
	}
}
