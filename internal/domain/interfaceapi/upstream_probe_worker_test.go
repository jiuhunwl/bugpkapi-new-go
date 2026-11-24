package interfaceapi

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

type probeContextDriver struct {
	started   chan context.Context
	release   chan struct{}
	queries   atomic.Int32
	active    atomic.Int32
	maxActive atomic.Int32
}
type probeContextConn struct{ driver *probeContextDriver }

var probeContextDriverID atomic.Uint64

func (d *probeContextDriver) Open(string) (driver.Conn, error) {
	return &probeContextConn{driver: d}, nil
}
func (*probeContextConn) Close() error              { return nil }
func (*probeContextConn) Begin() (driver.Tx, error) { return nil, errors.New("unused transaction") }
func (*probeContextConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unused statement")
}
func (c *probeContextConn) QueryContext(ctx context.Context, _ string, _ []driver.NamedValue) (driver.Rows, error) {
	c.driver.queries.Add(1)
	active := c.driver.active.Add(1)
	defer c.driver.active.Add(-1)
	for {
		old := c.driver.maxActive.Load()
		if active <= old || c.driver.maxActive.CompareAndSwap(old, active) {
			break
		}
	}
	select {
	case c.driver.started <- ctx:
	default:
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.driver.release:
		return nil, errors.New("test released query")
	}
}
func probeContextService(t *testing.T) (*Service, *probeContextDriver) {
	t.Helper()
	d := &probeContextDriver{started: make(chan context.Context, 8), release: make(chan struct{})}
	name := fmt.Sprintf("probe-context-%d", probeContextDriverID.Add(1))
	sql.Register(name, d)
	db, err := sql.Open(name, "")
	if err != nil {
		t.Fatal(err)
	}
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", MaxRetries: -1})
	t.Cleanup(func() { close(d.release); _ = db.Close(); _ = rdb.Close() })
	return &Service{repo: NewRepository(db), rdb: rdb}, d
}

func TestUpstreamProbeDoesNotStartAfterCancellation(t *testing.T) {
	service, d := probeContextService(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	worker := service.StartUpstreamProbe(ctx, time.Hour)
	defer worker.Close(context.Background())
	select {
	case <-d.started:
		t.Fatal("cancelled probe still started a database query")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestUpstreamProbeInitialSweepHonorsCancellationAndDeadline(t *testing.T) {
	service, d := probeContextService(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	worker := service.StartUpstreamProbe(ctx, time.Hour)
	defer worker.Close(context.Background())
	var queryCtx context.Context
	select {
	case queryCtx = <-d.started:
	case <-time.After(time.Second):
		t.Fatal("probe did not start")
	}
	if _, ok := queryCtx.Deadline(); !ok {
		t.Error("initial sweep has no deadline")
	}
	cancel()
	select {
	case <-queryCtx.Done():
	case <-time.After(100 * time.Millisecond):
		t.Error("initial sweep detached from shutdown cancellation")
	}
}

func TestUpstreamProbeCloseStopsLaterSweepsWithoutOverlap(t *testing.T) {
	service, d := probeContextService(t)
	worker := service.StartUpstreamProbe(context.Background(), 25*time.Millisecond)
	defer worker.Close(context.Background())
	for range 2 {
		select {
		case ctx := <-d.started:
			if _, ok := ctx.Deadline(); !ok {
				t.Fatal("periodic sweep has no deadline")
			}
		case <-time.After(time.Second):
			t.Fatal("periodic probe did not run")
		}
	}
	closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := worker.Close(closeCtx); err != nil {
		t.Fatal(err)
	}
	count := d.queries.Load()
	time.Sleep(75 * time.Millisecond)
	if d.queries.Load() != count || d.active.Load() != 0 {
		t.Fatal("probe continued after Close")
	}
	if d.maxActive.Load() != 1 {
		t.Fatalf("sweeps overlap: max active=%d", d.maxActive.Load())
	}
	if err := worker.Close(closeCtx); err != nil {
		t.Fatalf("repeated Close: %v", err)
	}
}

func TestDisabledUpstreamProbeReturnsNoWorker(t *testing.T) {
	if worker := new(Service).StartUpstreamProbe(nil, time.Minute); worker != nil {
		t.Fatal("probe without dependencies must stay disabled")
	}
	service, _ := probeContextService(t)
	if worker := service.StartUpstreamProbe(nil, 0); worker != nil {
		t.Fatal("zero interval must stay disabled")
	}
}
