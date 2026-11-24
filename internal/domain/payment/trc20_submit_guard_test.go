package payment

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestTrc20ConcurrentSubmitCooldown(t *testing.T) {
	guard := newTrc20SubmitLimiter(128, time.Minute)
	now := time.Now()
	for round := 0; round < 100; round++ {
		start := make(chan struct{})
		var ready, done sync.WaitGroup
		ready.Add(64)
		done.Add(64)
		var accepted atomic.Int32
		order := fmt.Sprintf("concurrent-cooldown-%d", round)
		for i := 0; i < 64; i++ {
			go func() {
				defer done.Done()
				ready.Done()
				<-start
				if guard.allow(order, now) {
					accepted.Add(1)
				}
			}()
		}
		ready.Wait()
		close(start)
		done.Wait()
		if accepted.Load() != 1 {
			t.Fatalf("round %d admitted %d simultaneous submissions, want exactly one", round, accepted.Load())
		}
	}
}

func TestTrc20SubmitGuardBoundsStorageWithoutEvictingActiveCooldowns(t *testing.T) {
	now := time.Unix(1000, 0)
	guard := newTrc20SubmitLimiter(2, 20*time.Second)
	if !guard.allow("A", now) || !guard.allow("B", now) {
		t.Fatal("initial submissions rejected")
	}
	if guard.allow("C", now) || guard.allow("A", now.Add(time.Second)) {
		t.Fatal("full guard evicted an active cooldown")
	}
	if len(guard.expires) != 2 {
		t.Fatalf("storage grew past capacity: %d", len(guard.expires))
	}
	if !guard.allow("C", now.Add(20*time.Second)) {
		t.Fatal("expired entries were not reclaimed")
	}
	if len(guard.expires) != 1 {
		t.Fatalf("expired keys retained: %d", len(guard.expires))
	}
	if !guard.allow("A", now.Add(20*time.Second)) {
		t.Fatal("expired original order did not regain access")
	}
	if guard.allow("", now) || guard.allow(strings.Repeat("x", 65), now) {
		t.Fatal("invalid keys retained")
	}
}
