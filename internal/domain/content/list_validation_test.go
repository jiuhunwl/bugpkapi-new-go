package content

import (
	"strings"
	"testing"
)

func TestValidateContentListParamsRejectsUnsafeFilters(t *testing.T) {
	invalid := 2
	for name, params := range map[string]ListParams{
		"keyword": {Keyword: strings.Repeat("公", 129), Page: 1, PageSize: 20},
		"page":    {Page: 0, PageSize: 20},
		"popup":   {IsPopup: &invalid, Page: 1, PageSize: 20},
		"size":    {Page: 1, PageSize: 101},
		"status":  {Page: 1, PageSize: 20, Status: &invalid},
		"top":     {IsTop: &invalid, Page: 1, PageSize: 20},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateListParams(&params); err == nil {
				t.Fatal("unsafe content-list filter was accepted")
			}
		})
	}
}
