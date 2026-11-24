package redisutil

import (
	"testing"
	"time"
)

func TestTTLMillisecondsRoundsTinyPositiveDurations(t *testing.T) {
	if got := ttlMilliseconds(time.Nanosecond); got != 1 {
		t.Fatalf("ttlMilliseconds(time.Nanosecond) = %d, want 1", got)
	}
	if got := ttlMilliseconds(time.Second); got != 1000 {
		t.Fatalf("ttlMilliseconds(time.Second) = %d, want 1000", got)
	}
	if got := ttlMilliseconds(0); got != 0 {
		t.Fatalf("ttlMilliseconds(0) = %d, want 0", got)
	}
}
