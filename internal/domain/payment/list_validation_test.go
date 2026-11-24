package payment

import (
	"strings"
	"testing"
)

func TestNormalizePaymentListParamsRejectsUnsafeFilters(t *testing.T) {
	for name, test := range map[string]struct {
		orders bool
		params ListParams
	}{
		"keyword":       {orders: true, params: ListParams{Keyword: strings.Repeat("单", 129), Page: 1, PageSize: 20}},
		"order type":    {orders: true, params: ListParams{OrderType: "UNKNOWN", Page: 1, PageSize: 20}},
		"page":          {orders: true, params: ListParams{Page: 0, PageSize: 20}},
		"preset status": {params: ListParams{Page: 1, PageSize: 20, Status: "2"}},
		"size":          {orders: true, params: ListParams{Page: 1, PageSize: 101}},
		"status":        {orders: true, params: ListParams{Page: 1, PageSize: 20, Status: "UNKNOWN"}},
	} {
		t.Run(name, func(t *testing.T) {
			params := test.params
			if err := normalizeListParams(&params, test.orders); err == nil {
				t.Fatal("unsafe payment-list filter was accepted")
			}
		})
	}
}
