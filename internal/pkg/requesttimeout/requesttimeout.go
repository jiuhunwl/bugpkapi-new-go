// Package requesttimeout contains the shared request-timeout contract used by
// the administration API and the open gateway. Values are persisted as
// integer milliseconds so the configuration is unambiguous across JSON and
// SQL boundaries.
package requesttimeout

import (
	"fmt"
	"time"
)

const (
	// DefaultMilliseconds is the safe timeout used when an older installation
	// has no global timeout row/value yet.
	DefaultMilliseconds = 15_000
	// MinMilliseconds prevents a configuration from creating an unusably short
	// request budget while still allowing sub-second APIs.
	MinMilliseconds = 100
	// MaxMilliseconds bounds resource consumption for an individual upstream
	// request. The base HTTP client is configured with this same ceiling.
	MaxMilliseconds = 120_000
)

// Normalize validates a persisted/API value. A zero value is accepted only
// when allowZero is true; for interface-local settings zero means inherit the
// global timeout.
func Normalize(value int, allowZero bool) (int, error) {
	if value == 0 && allowZero {
		return 0, nil
	}
	if value < MinMilliseconds || value > MaxMilliseconds {
		return 0, fmt.Errorf("request timeout must be between %d and %d milliseconds", MinMilliseconds, MaxMilliseconds)
	}
	return value, nil
}

// ResolveMilliseconds returns the effective timeout. An interface override
// wins only when it is enabled and contains a valid non-zero value; otherwise
// the global value is used, followed by the safe default for legacy/malformed
// rows.
func ResolveMilliseconds(interfaceValue, globalValue, interfaceOverride int) int {
	if interfaceOverride == 1 && interfaceValue >= MinMilliseconds && interfaceValue <= MaxMilliseconds {
		return interfaceValue
	}
	if globalValue >= MinMilliseconds && globalValue <= MaxMilliseconds {
		return globalValue
	}
	return DefaultMilliseconds
}

// Duration is ResolveMilliseconds expressed as a Go duration for context
// deadlines and HTTP requests.
func Duration(interfaceValue, globalValue, interfaceOverride int) time.Duration {
	return time.Duration(ResolveMilliseconds(interfaceValue, globalValue, interfaceOverride)) * time.Millisecond
}
