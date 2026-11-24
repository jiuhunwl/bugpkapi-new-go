package interfaceapi

import "testing"

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

func TestNormalizePointsEnforcesOperationalLimit(t *testing.T) {
	if got, err := normalizePoints(maxInterfacePointPrice); err != nil || got != maxInterfacePointPrice {
		t.Fatalf("normalizePoints(max) = %d, %v", got, err)
	}
	if _, err := normalizePoints("1000000000000001"); err == nil {
		t.Fatal("normalizePoints accepted a value above the operational limit")
	}
}
