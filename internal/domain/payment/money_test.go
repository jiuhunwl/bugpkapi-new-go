package payment

import (
	"strings"
	"testing"
)

func TestNormalizeMoneyKeepsDecimalPrecision(t *testing.T) {
	got, err := normalizeMoney("12345678.12345")
	if err != nil {
		t.Fatalf("normalizeMoney returned error: %v", err)
	}
	want := "12345678.1235"
	if got != want {
		t.Fatalf("normalizeMoney() = %q, want %q", got, want)
	}
}

func TestNormalizeMoneyRejectsDatabaseOverflowAndHugeInput(t *testing.T) {
	for _, value := range []string{
		"100000000.0000",
		"99999999999999999999999999999999999999999999999999999999999999999",
	} {
		if _, err := normalizeMoney(value); err == nil {
			t.Fatalf("normalizeMoney accepted out-of-range value %q", value)
		}
	}
}

func TestNormalizeMoneyAcceptsDecimalAndExponentButRejectsFractionSyntax(t *testing.T) {
	for input, expected := range map[string]string{
		"0":       "0.0000",
		"001.25":  "1.2500",
		"1e2":     "100.0000",
		"2.5E-1":  "0.2500",
		" 10.50 ": "10.5000",
	} {
		got, err := normalizeMoney(input)
		if err != nil || got != expected {
			t.Fatalf("normalizeMoney(%q) = %q, %v; want %q", input, got, err, expected)
		}
	}
	for _, input := range []string{"1/2", "+1", ".5", "1.", "1e", "--1", "1e1001", "1e-1001"} {
		if _, err := normalizeMoney(input); err == nil {
			t.Fatalf("normalizeMoney accepted non-decimal syntax %q", input)
		}
	}
}

func TestNormalizePresetEnforcesSchemaBounds(t *testing.T) {
	status := 1
	valid := SaveRechargePresetRequest{Name: "Recharge", Amount: "10", GiftAmount: "1", SortOrder: 1, Status: &status}
	if _, err := normalizePreset(valid); err != nil {
		t.Fatalf("valid preset rejected: %v", err)
	}

	invalid := []SaveRechargePresetRequest{
		{Name: strings.Repeat("名", maxRechargePresetName+1), Amount: "10", Status: &status},
		{Name: "Recharge", Amount: "10", SortOrder: -1, Status: &status},
		{Name: "Recharge", Amount: "10", SortOrder: maxRechargePresetSortNo + 1, Status: &status},
		{Name: "Recharge", Amount: "100000000", Status: &status},
		{Name: "Recharge", Amount: "60000000", GiftAmount: "50000000", Status: &status},
	}
	for index, request := range invalid {
		if _, err := normalizePreset(request); err == nil {
			t.Fatalf("invalid preset %d was accepted", index)
		}
	}
}

func TestAddMoneyKeepsDecimalPrecision(t *testing.T) {
	got := addMoney("99999999999999999999.9999", "0.0001")
	want := "100000000000000000000.0000"
	if got != want {
		t.Fatalf("addMoney() = %q, want %q", got, want)
	}
}
