package gateway

import "testing"

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
