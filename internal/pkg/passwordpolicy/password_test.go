package passwordpolicy

import (
	"strings"
	"testing"
)

func TestValidPasswordBoundaries(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  bool
	}{
		{name: "too short", value: "1234567", want: false},
		{name: "minimum", value: "12345678", want: true},
		{name: "maximum characters", value: strings.Repeat("a", MaxCharacters), want: true},
		{name: "too many characters", value: strings.Repeat("a", MaxCharacters+1), want: false},
		{name: "bcrypt byte overflow", value: strings.Repeat("密", 25), want: false},
		{name: "unicode within byte limit", value: strings.Repeat("密", 24), want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := Valid(test.value); got != test.want {
				t.Fatalf("Valid(%q) = %v, want %v", test.value, got, test.want)
			}
		})
	}
}

func TestWithinMaximumAllowsLegacyShortPasswordsButRejectsBcryptOverflow(t *testing.T) {
	if !WithinMaximum("short") {
		t.Fatal("legacy short password should remain login-compatible")
	}
	if WithinMaximum(strings.Repeat("x", MaxBytes+1)) {
		t.Fatal("password exceeding bcrypt byte limit was accepted")
	}
}
