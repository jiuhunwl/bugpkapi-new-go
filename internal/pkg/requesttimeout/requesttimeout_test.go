package requesttimeout

import (
	"testing"
	"time"
)

func TestResolveMillisecondsUsesInterfaceOverride(t *testing.T) {
	if got := ResolveMilliseconds(2500, 15000, 1); got != 2500 {
		t.Fatalf("ResolveMilliseconds() = %d, want 2500", got)
	}
	if got := Duration(2500, 15000, 1); got != 2500*time.Millisecond {
		t.Fatalf("Duration() = %s, want 2.5s", got)
	}
}

func TestResolveMillisecondsFallsBackToGlobalWhenInterfaceIsNotConfigured(t *testing.T) {
	for _, override := range []int{0, 1} {
		if got := ResolveMilliseconds(0, 12000, override); got != 12000 {
			t.Fatalf("ResolveMilliseconds(0, 12000, %d) = %d, want 12000", override, got)
		}
	}
	if got := ResolveMilliseconds(2500, 12000, 0); got != 12000 {
		t.Fatalf("non-overridden interface timeout = %d, want global 12000", got)
	}
}

func TestResolveMillisecondsUsesSafeDefaultWhenGlobalIsMissing(t *testing.T) {
	if got := ResolveMilliseconds(0, 0, 0); got != DefaultMilliseconds {
		t.Fatalf("missing global timeout = %d, want %d", got, DefaultMilliseconds)
	}
	if got := Duration(0, 0, 0); got != time.Duration(DefaultMilliseconds)*time.Millisecond {
		t.Fatalf("missing global duration = %s, want %dms", got, DefaultMilliseconds)
	}
}

func TestNormalizeRejectsOutOfRangeValuesAndAllowsInterfaceInheritanceMarker(t *testing.T) {
	if got, err := Normalize(0, true); err != nil || got != 0 {
		t.Fatalf("Normalize(0, true) = %d, %v, want 0, nil", got, err)
	}
	if _, err := Normalize(0, false); err == nil {
		t.Fatal("Normalize(0, false) should reject an unset global timeout")
	}
	for _, value := range []int{-1, MinMilliseconds - 1, MaxMilliseconds + 1} {
		if _, err := Normalize(value, true); err == nil {
			t.Fatalf("Normalize(%d, true) should reject out-of-range value", value)
		}
	}
	if got, err := Normalize(MaxMilliseconds, false); err != nil || got != MaxMilliseconds {
		t.Fatalf("Normalize(max) = %d, %v", got, err)
	}
}

func TestResolveMillisecondsPriorityAndBoundaryRegression(t *testing.T) {
	tests := []struct {
		name                          string
		local, global, override, want int
	}{
		{"local may exceed global", 120000, 100, 1, 120000},
		{"local may be shorter than global", 100, 120000, 1, 100},
		{"disabled local is retained but ignored", 120000, 100, 0, 100},
		{"zero inherits even with override enabled", 0, 120000, 1, 120000},
		{"missing local inherits global", 0, 100, 0, 100},
		{"invalid local low falls back", 99, 12000, 1, 12000},
		{"invalid local high falls back", 120001, 12000, 1, 12000},
		{"invalid override flag falls back", 60000, 12000, 2, 12000},
		{"valid local survives missing global", 120000, 0, 1, 120000},
		{"missing global uses default", 0, 0, 0, 15000},
		{"invalid global low uses default", 0, 99, 1, 15000},
		{"invalid global high uses default", 0, 120001, 0, 15000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ResolveMilliseconds(tt.local, tt.global, tt.override); got != tt.want {
				t.Fatalf("resolved timeout = %d, want %d", got, tt.want)
			}
			if got := Duration(tt.local, tt.global, tt.override); got != time.Duration(tt.want)*time.Millisecond {
				t.Fatalf("timeout duration = %v, want %dms", got, tt.want)
			}
		})
	}
}

func TestInheritedRequestTimeoutFollowsGlobalChangesWithoutChangingLocalValue(t *testing.T) {
	for _, global := range []int{100, 30000, 120000} {
		if got := ResolveMilliseconds(60000, global, 0); got != global {
			t.Fatalf("disabled override resolved to %d after global changed to %d", got, global)
		}
		if got := ResolveMilliseconds(0, global, 1); got != global {
			t.Fatalf("zero override resolved to %d after global changed to %d", got, global)
		}
		if got := ResolveMilliseconds(60000, global, 1); got != 60000 {
			t.Fatalf("enabled override was changed to %d by global %d", got, global)
		}
	}
}

func TestNormalizeAcceptsExactTimeoutBoundsForBothScopes(t *testing.T) {
	for _, allowZero := range []bool{false, true} {
		for _, value := range []int{100, 120000} {
			if got, err := Normalize(value, allowZero); err != nil || got != value {
				t.Fatalf("Normalize(%d, %t) = %d, %v", value, allowZero, got, err)
			}
		}
		for _, value := range []int{-1, 1, 99, 120001} {
			if _, err := Normalize(value, allowZero); err == nil {
				t.Fatalf("Normalize(%d, %t) accepted an out-of-range value", value, allowZero)
			}
		}
	}
}
