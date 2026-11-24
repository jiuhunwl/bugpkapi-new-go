package observability

import (
	"sync"
	"time"
)

const (
	defaultFuzzyWindow = 10 * time.Second
	defaultFuzzyBurst  = 20
)

// fuzzyRateLimiter 以主体为粒度限制模糊搜索频率。纯进程内实现：
// 单实例部署下足够；多实例时各实例独立限额（模糊搜索是受控兜底路径，可接受）。
type fuzzyRateLimiter struct {
	mu     sync.Mutex
	window time.Duration
	burst  int
	hits   map[int64][]time.Time
}

func newFuzzyRateLimiter(window time.Duration, burst int) *fuzzyRateLimiter {
	if window <= 0 {
		window = defaultFuzzyWindow
	}
	if burst <= 0 {
		burst = defaultFuzzyBurst
	}
	return &fuzzyRateLimiter{window: window, burst: burst, hits: make(map[int64][]time.Time)}
}

func (l *fuzzyRateLimiter) allow(principalID int64, now time.Time) bool {
	if l == nil {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	cutoff := now.Add(-l.window)
	kept := l.hits[principalID][:0]
	for _, hit := range l.hits[principalID] {
		if hit.After(cutoff) {
			kept = append(kept, hit)
		}
	}
	if len(kept) == 0 {
		delete(l.hits, principalID)
	} else {
		l.hits[principalID] = kept
	}
	if len(kept) >= l.burst {
		return false
	}
	l.hits[principalID] = append(l.hits[principalID], now)
	return true
}
