package app

import (
	"context"
	"fmt"
	"sync"
	"time"
)

type dependencyPing func(context.Context) error

type readinessCall struct {
	done chan struct{}
	err  error
}

type readinessProbe struct {
	cacheTTL  time.Duration
	cachedErr error
	cachedTo  time.Time
	call      *readinessCall
	mysqlPing dependencyPing
	mu        sync.Mutex
	now       func() time.Time
	redisPing dependencyPing
	timeout   time.Duration
}

func newReadinessProbe(mysqlPing, redisPing dependencyPing) *readinessProbe {
	return &readinessProbe{
		cacheTTL:  time.Second,
		mysqlPing: mysqlPing,
		now:       time.Now,
		redisPing: redisPing,
		timeout:   time.Second,
	}
}

func (p *readinessProbe) Check(ctx context.Context) error {
	if p == nil {
		return fmt.Errorf("readiness probe is not configured")
	}
	now := p.now()
	p.mu.Lock()
	if now.Before(p.cachedTo) {
		err := p.cachedErr
		p.mu.Unlock()
		return err
	}
	if call := p.call; call != nil {
		p.mu.Unlock()
		select {
		case <-call.done:
			return call.err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	call := &readinessCall{done: make(chan struct{})}
	p.call = call
	p.mu.Unlock()

	probeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), p.timeout)
	err := checkReadiness(probeCtx, p.mysqlPing, p.redisPing)
	cancel()

	p.mu.Lock()
	call.err = err
	p.cachedErr = err
	p.cachedTo = p.now().Add(p.cacheTTL)
	p.call = nil
	close(call.done)
	p.mu.Unlock()
	return err
}

func checkReadiness(ctx context.Context, mysqlPing, redisPing dependencyPing) error {
	if mysqlPing == nil || redisPing == nil {
		return fmt.Errorf("readiness dependency is not configured")
	}
	if err := mysqlPing(ctx); err != nil {
		return fmt.Errorf("mysql is unavailable: %w", err)
	}
	if err := redisPing(ctx); err != nil {
		return fmt.Errorf("redis is unavailable: %w", err)
	}
	return nil
}
