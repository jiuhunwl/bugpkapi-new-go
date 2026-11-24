package packagepkg

import (
	"strings"
	"testing"
)

func TestNormalizeMoneyKeepsDatabasePrecision(t *testing.T) {
	got, err := normalizeMoney("99999999.9999")
	if err != nil {
		t.Fatalf("normalizeMoney returned error: %v", err)
	}
	if got != "99999999.9999" {
		t.Fatalf("normalizeMoney() = %q", got)
	}
	if _, err := normalizeMoney("100000000.0000"); err == nil {
		t.Fatal("normalizeMoney accepted a value outside decimal(12,4)")
	}
}

func TestNormalizeMoneyRejectsNonCanonicalAndExtremeInputs(t *testing.T) {
	for _, input := range []string{"1/2", "+1", ".5", "1.", "1e1001", "1e-1001"} {
		if _, err := normalizeMoney(input); err == nil {
			t.Fatalf("normalizeMoney accepted invalid input %q", input)
		}
	}
}

func TestPackageValidationRejectsDatabaseOverflowAndOversizedPayloads(t *testing.T) {
	tooLong := strings.Repeat("a", 129)
	if _, err := normalizeGlobal(SaveGlobalPackageRequest{Name: tooLong, Price: "1", ValidDays: 30}); err == nil {
		t.Fatal("normalizeGlobal accepted an oversized name")
	}
	if _, err := normalizePoint(SavePointPackageRequest{Name: "points", Price: "1", Points: maxPackagePoints + 1}); err == nil {
		t.Fatal("normalizePoint accepted an excessive point grant")
	}
	specs := make([]SaveInterfacePackageSpecRequest, maxPackageSpecs+1)
	for index := range specs {
		specs[index] = SaveInterfacePackageSpecRequest{Name: "spec", Price: "1", ValidDays: 30}
	}
	if _, err := normalizeInterface(SaveInterfacePackageRequest{InterfaceID: 1, Name: "api", Specs: specs}); err == nil {
		t.Fatal("normalizeInterface accepted too many package specifications")
	}
}

func TestNegativeMoneyDoesNotCreateNegativeZero(t *testing.T) {
	tests := map[string]string{
		"0":       "0.0000",
		"0.0000":  "0.0000",
		"1.2300":  "-1.2300",
		"-2.0000": "-2.0000",
	}
	for input, want := range tests {
		if got := negativeMoney(input); got != want {
			t.Fatalf("negativeMoney(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestNormalizePointAcceptsPointAmountAlias(t *testing.T) {
	got, err := normalizePoint(SavePointPackageRequest{
		Name:        "开发点数包",
		PointAmount: 100,
		Price:       "9.9",
	})
	if err != nil {
		t.Fatalf("normalizePoint returned error: %v", err)
	}
	if got.Points != 100 || got.PointAmount != 100 {
		t.Fatalf("normalizePoint points = %d, pointAmount = %d, want both 100", got.Points, got.PointAmount)
	}
}

func TestNormalizePointAcceptsStringPointAmount(t *testing.T) {
	got, err := normalizePoint(SavePointPackageRequest{
		Name:        "string points",
		PointAmount: "1,000",
		Price:       "9.9",
	})
	if err != nil {
		t.Fatalf("normalizePoint returned error: %v", err)
	}
	if got.Points != 1000 || got.PointAmount != 1000 {
		t.Fatalf("normalizePoint points = %d, pointAmount = %d, want both 1000", got.Points, got.PointAmount)
	}
}

func TestNormalizePointRejectsFractionalPointAmount(t *testing.T) {
	_, err := normalizePoint(SavePointPackageRequest{
		Name:        "fractional points",
		PointAmount: "1.5",
		Price:       "9.9",
	})
	if err == nil {
		t.Fatal("normalizePoint returned nil error for fractional points")
	}
}

func TestNormalizePointRejectsNonCanonicalPointAmount(t *testing.T) {
	for _, value := range []string{"1/1", "+1", "1e3", "1,00", "1,,000"} {
		if _, err := normalizePoint(SavePointPackageRequest{
			Name:        "invalid point amount",
			PointAmount: value,
			Price:       "9.9",
		}); err == nil {
			t.Fatalf("normalizePoint accepted invalid point amount %q", value)
		}
	}
}

func TestInterfacePackageWhereFiltersDisabledInterfacesForPublicMall(t *testing.T) {
	got := interfacePackageWhere(true)
	want := []string{"where 1=1", "p.status=1", "a.status=1", "p.hidden=0"}
	if len(got) != len(want) {
		t.Fatalf("interfacePackageWhere(true) length = %d, want %d: %#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("interfacePackageWhere(true)[%d] = %q, want %q", i, got[i], want[i])
		}
	}

	got = interfacePackageWhere(false)
	if len(got) != 1 || got[0] != "where 1=1" {
		t.Fatalf("interfacePackageWhere(false) = %#v, want only base where clause", got)
	}
}
