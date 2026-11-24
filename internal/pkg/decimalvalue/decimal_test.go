package decimalvalue

import (
	"errors"
	"testing"
)

func TestParseNonNegativeAcceptsCanonicalDecimals(t *testing.T) {
	tests := map[string]string{
		"0":       "0",
		"001.25":  "5/4",
		"12.3400": "617/50",
		"1e2":     "100",
		"1E-2":    "1/100",
	}
	for input, want := range tests {
		got, err := ParseNonNegative(input)
		if err != nil {
			t.Fatalf("ParseNonNegative(%q): %v", input, err)
		}
		if got.RatString() != want {
			t.Fatalf("ParseNonNegative(%q) = %s, want %s", input, got.RatString(), want)
		}
	}
}

func TestParseNonNegativeRejectsNonCanonicalOrExpensiveInputs(t *testing.T) {
	tests := []struct {
		input string
		want  error
	}{
		{input: "", want: ErrFormat},
		{input: "-1", want: ErrNegative},
		{input: "+1", want: ErrFormat},
		{input: "1/2", want: ErrFormat},
		{input: ".5", want: ErrFormat},
		{input: "1.", want: ErrFormat},
		{input: "NaN", want: ErrFormat},
		{input: "Inf", want: ErrFormat},
		{input: "1e1001", want: ErrExponentRange},
		{input: "1e-1001", want: ErrExponentRange},
		{input: "12345678901234567890123456789012345678901234567890123456789012345", want: ErrTextTooLong},
	}
	for _, test := range tests {
		_, err := ParseNonNegative(test.input)
		if !errors.Is(err, test.want) {
			t.Fatalf("ParseNonNegative(%q) error = %v, want %v", test.input, err, test.want)
		}
	}
}

func TestParseSupportsBoundedSignedDecimalsExactly(t *testing.T) {
	left, err := Parse("-9007199254740993")
	if err != nil {
		t.Fatal(err)
	}
	right, err := Parse("-9007199254740992")
	if err != nil {
		t.Fatal(err)
	}
	if left.Cmp(right) >= 0 {
		t.Fatalf("comparison lost integer precision: %s >= %s", left, right)
	}
	if _, err := Parse("1e1001"); !errors.Is(err, ErrExponentRange) {
		t.Fatalf("large signed exponent error = %v", err)
	}
}
