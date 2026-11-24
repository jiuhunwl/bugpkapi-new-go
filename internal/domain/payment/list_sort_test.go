package payment

import (
	"strings"
	"testing"
)

func TestPaymentOrderOrderByWhitelist(t *testing.T) {
	if got := paymentOrderOrderBy(ListParams{}); got != "o.id desc" {
		t.Fatalf("default order = %q", got)
	}
	if got := paymentOrderOrderBy(ListParams{SortBy: "pay_amount", SortOrder: "asc"}); got != "o.pay_amount asc,o.id desc" {
		t.Fatalf("pay_amount asc order = %q", got)
	}
	if got := paymentOrderOrderBy(ListParams{SortBy: "paid_at"}); !strings.Contains(got, "o.paid_at is null") {
		t.Fatalf("paid_at order = %q", got)
	}
	if got := paymentOrderOrderBy(ListParams{SortBy: "status"}); !strings.Contains(got, "case o.status") {
		t.Fatalf("status order = %q", got)
	}
}

func TestPaymentOrderListFilters(t *testing.T) {
	params := ListParams{
		MaxAmount: "100",
		MinAmount: "10",
		PaidFrom:  "2026-01-01",
		PaidTo:    "2026-01-31",
		Page:      1,
		PageSize:  20,
		SortBy:    " Pay_Amount ",
		SortOrder: " ASC ",
	}
	if err := normalizeListParams(&params, true); err != nil {
		t.Fatal(err)
	}
	if params.SortBy != "pay_amount" || params.SortOrder != "asc" {
		t.Fatalf("normalized sort = %+v", params)
	}
	if params.MinAmount != "10.0000" || params.MaxAmount != "100.0000" {
		t.Fatalf("normalized amounts = %+v", params)
	}

	for name, invalid := range map[string]ListParams{
		"unknown sort":    {Page: 1, PageSize: 20, SortBy: "payload"},
		"bad direction":   {Page: 1, PageSize: 20, SortOrder: "sideways"},
		"bad paid date":   {Page: 1, PageSize: 20, PaidFrom: "2026/01/01"},
		"reversed paid":   {Page: 1, PageSize: 20, PaidFrom: "2026-02-01", PaidTo: "2026-01-01"},
		"bad amount":      {Page: 1, PageSize: 20, MinAmount: "abc"},
		"reversed amount": {Page: 1, PageSize: 20, MinAmount: "100", MaxAmount: "10"},
	} {
		t.Run(name, func(t *testing.T) {
			value := invalid
			if err := normalizeListParams(&value, true); err == nil {
				t.Fatalf("params unexpectedly accepted: %+v", invalid)
			}
		})
	}
}
