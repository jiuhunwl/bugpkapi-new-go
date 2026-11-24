package main

import (
	"testing"
	"time"
)

func TestLatencyPercentilesUseBoundedHistogram(t *testing.T) {
	s := &stats{statusCounts: map[int]int64{}}
	if s.percentile(95) != 0 {
		t.Fatal("empty percentile is not zero")
	}
	for i := 1; i <= 100; i++ {
		s.record(200, time.Duration(i)*time.Millisecond, nil)
	}
	for _, p := range []int{50, 95, 99, 100} {
		if got := s.percentile(p); got != time.Duration(p)*time.Millisecond {
			t.Fatalf("p%d=%s", p, got)
		}
	}
	s = &stats{statusCounts: map[int]int64{}}
	s.record(200, 1500*time.Microsecond, nil)
	if got := s.percentile(99); got != 2*time.Millisecond {
		t.Fatalf("sub-ms bucket must round up: %s", got)
	}
	s = &stats{statusCounts: map[int]int64{}}
	s.record(500, time.Minute, nil)
	if got := s.percentile(99); got < time.Minute {
		t.Fatalf("overflow understated tail: %s", got)
	}
}
