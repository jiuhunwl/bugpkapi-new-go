package app

import (
	"context"
	"sync"

	"bugpk-api/internal/pkg/response"
	hertzapp "github.com/cloudwego/hertz/pkg/app"
)

// requestDrain tracks handler completion, not listener/connection closure.
type requestDrain struct {
	mu                 sync.Mutex
	next               uint64
	active             map[uint64]context.CancelFunc
	stopping, finished bool
	done               chan struct{}
}

func newRequestDrain() *requestDrain {
	return &requestDrain{active: make(map[uint64]context.CancelFunc), done: make(chan struct{})}
}
func (d *requestDrain) middleware() hertzapp.HandlerFunc {
	return func(ctx context.Context, c *hertzapp.RequestContext) {
		d.mu.Lock()
		if d.stopping {
			d.mu.Unlock()
			response.Fail(c, 503, "服务正在关闭")
			c.Abort()
			return
		}
		ctx, cancel := context.WithCancel(ctx)
		d.next++
		id := d.next
		d.active[id] = cancel
		d.mu.Unlock()
		defer func() { cancel(); d.mu.Lock(); delete(d.active, id); d.finishLocked(); d.mu.Unlock() }()
		c.Next(ctx)
	}
}
func (d *requestDrain) finishLocked() {
	if d.stopping && len(d.active) == 0 && !d.finished {
		d.finished = true
		close(d.done)
	}
}
func (d *requestDrain) stop() { d.mu.Lock(); d.stopping = true; d.finishLocked(); d.mu.Unlock() }
func (d *requestDrain) wait(ctx context.Context) error {
	d.stop()
	select {
	case <-d.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (d *requestDrain) cancelActive() {
	d.mu.Lock()
	callbacks := make([]context.CancelFunc, 0, len(d.active))
	for _, cancel := range d.active {
		callbacks = append(callbacks, cancel)
	}
	d.mu.Unlock()
	for _, cancel := range callbacks {
		cancel()
	}
}
