package payment

import (
	"strings"
	"sync"
	"time"
)

const trc20SubmitGuardCapacity = 4096

// This is a bounded per-process guard, not a claim of cluster-wide rate limiting.
// Active entries are never evicted to admit a new key, which would bypass the
// existing order's cooldown under churn.
type trc20SubmitLimiter struct {
	mu        sync.Mutex
	expires   map[string]time.Time
	nextSweep time.Time
	capacity  int
	cooldown  time.Duration
}

func newTrc20SubmitLimiter(capacity int, cooldown time.Duration) *trc20SubmitLimiter {
	return &trc20SubmitLimiter{expires: make(map[string]time.Time), capacity: capacity, cooldown: cooldown}
}

func (l *trc20SubmitLimiter) allow(orderNo string, now time.Time) bool {
	orderNo = strings.TrimSpace(orderNo)
	if l == nil || orderNo == "" || len(orderNo) > 64 || l.capacity <= 0 || l.cooldown <= 0 {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if !now.Before(l.nextSweep) {
		for key, expiry := range l.expires {
			if !now.Before(expiry) {
				delete(l.expires, key)
			}
		}
		l.nextSweep = now.Add(time.Second)
	}
	if expiry, ok := l.expires[orderNo]; ok {
		if now.Before(expiry) {
			return false
		}
		delete(l.expires, orderNo)
	}
	if len(l.expires) >= l.capacity {
		return false
	}
	l.expires[orderNo] = now.Add(l.cooldown)
	return true
}

var trc20SubmitGuard = newTrc20SubmitLimiter(trc20SubmitGuardCapacity, trc20SubmitCooldown)

func allowTrc20Submit(orderNo string) bool { return trc20SubmitGuard.allow(orderNo, time.Now()) }
