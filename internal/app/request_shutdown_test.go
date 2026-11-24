package app

import (
	"context"
	"database/sql/driver"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"bugpk-api/internal/config"
	"bugpk-api/internal/domain/gateway"
	"bugpk-api/internal/pkg/testdb"
	hertzapp "github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
)

func TestApplicationRunNeverReportsSuccessfulTimedOutHTTPDrain(t *testing.T) {
	for attempt := 0; attempt < 12; attempt++ {
		port := reserveTCPPort(t)
		cfg := lifecycleServerConfig(port)
		cfg.ShutdownTimeout = 20 * time.Millisecond
		requests := newRequestDrain()
		application := &Application{Config: config.Config{Server: cfg}, Server: newHTTPServer(cfg, requests), requests: requests}
		started, release := make(chan struct{}), make(chan struct{})
		var releaseOnce sync.Once
		releaseHandler := func() { releaseOnce.Do(func() { close(release) }) }
		application.Server.GET("/slow", func(_ context.Context, c *hertzapp.RequestContext) { close(started); <-release; c.SetStatusCode(200) })
		ctx, cancel := context.WithCancel(context.Background())
		runDone := make(chan error, 1)
		go func() { runDone <- application.Run(ctx) }()
		addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
		limit := time.Now().Add(time.Second)
		for {
			connection, err := net.DialTimeout("tcp", addr, 20*time.Millisecond)
			if err == nil {
				connection.Close()
				break
			}
			if time.Now().After(limit) {
				cancel()
				releaseHandler()
				t.Fatal("test HTTP server did not start")
			}
			time.Sleep(time.Millisecond)
		}
		responseDone := make(chan struct{})
		go func() {
			defer close(responseDone)
			client := &http.Client{Timeout: 2 * time.Second}
			response, err := client.Get("http://" + addr + "/slow")
			if err == nil {
				_, _ = io.Copy(io.Discard, response.Body)
				response.Body.Close()
			}
		}()
		select {
		case <-started:
		case <-time.After(time.Second):
			cancel()
			releaseHandler()
			t.Fatal("request did not start")
		}
		cancel()
		err := <-runDone
		releaseHandler()
		<-responseDone
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("attempt %d reported successful/incorrect drain while handler was active: %v", attempt, err)
		}
	}
}

func TestApplicationCloseBudgetIncludesConnectionClose(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	db := testdb.OpenStub(t, testdb.SQLStub{Close: func() error { once.Do(func() { close(entered) }); <-release; return nil }})
	if err := db.PingContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	application := &Application{Config: config.Config{Server: config.ServerConfig{ShutdownTimeout: 30 * time.Millisecond}}, DB: db}
	done := make(chan error, 1)
	go func() { done <- application.Close() }()
	<-entered
	select {
	case err := <-done:
		close(release)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("close hid timeout: %v", err)
		}
	case <-time.After(120 * time.Millisecond):
		close(release)
		<-done
		t.Fatal("connection close exceeded application wait budget")
	}
	if err := application.Close(); err != nil {
		t.Fatalf("completed connection cleanup did not finish: %v", err)
	}
}

func TestTimedOutHTTPProducerRetainsDependenciesUntilItFinishes(t *testing.T) {
	port := reserveTCPPort(t)
	cfg := lifecycleServerConfig(port)
	cfg.ShutdownTimeout = 30 * time.Millisecond
	var mu sync.Mutex
	var amounts []string
	db := testdb.OpenStub(t, testdb.SQLStub{Exec: func(_ context.Context, _ string, args []driver.NamedValue) (driver.Result, error) {
		mu.Lock()
		amounts = append(amounts, args[0].Value.(string))
		mu.Unlock()
		return driver.RowsAffected(1), nil
	}})
	var pending atomic.Bool
	revenue := gateway.NewRevenueSnapshot(lifecycleRevenueProjector(func(ctx context.Context, _ int) (int, error) {
		if pending.Swap(false) {
			_, err := db.ExecContext(ctx, "test durable projection", "1.2500")
			return 1, err
		}
		return 0, nil
	})).Start(context.Background(), time.Hour)
	tracker := newRequestDrain()
	application := &Application{Config: config.Config{Server: cfg}, DB: db, Server: newHTTPServer(cfg, tracker), requests: tracker, backgroundClosers: []func(context.Context) error{revenue.CloseContext}}
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	accepted := make(chan bool, 1)
	application.Server.GET("/late-charge", func(_ context.Context, c *hertzapp.RequestContext) {
		close(started)
		<-release
		pending.Store(true)
		accepted <- revenue.NotifyConfirmed()
		c.SetStatusCode(200)
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- application.Run(ctx) }()
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	limit := time.Now().Add(time.Second)
	for {
		conn, err := net.DialTimeout("tcp", addr, 20*time.Millisecond)
		if err == nil {
			conn.Close()
			break
		}
		if time.Now().After(limit) {
			t.Fatal("HTTP server did not start")
		}
		time.Sleep(time.Millisecond)
	}
	responseDone := make(chan struct{})
	go func() {
		defer close(responseDone)
		client := &http.Client{Timeout: 2 * time.Second}
		resp, err := client.Get("http://" + addr + "/late-charge")
		if err == nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("request did not enter")
	}
	cancel()
	if err := <-runDone; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("drain timeout hidden: %v", err)
	}
	if err := application.Close(); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("active producer was reported closed: %v", err)
	}
	if err := db.PingContext(context.Background()); err != nil {
		t.Fatalf("database closed under an active producer: %v", err)
	}
	unblock()
	if !<-accepted {
		t.Fatal("sink closed under an active producer")
	}
	<-responseDone
	if err := application.Close(); err != nil {
		t.Fatalf("finished producer could not be drained: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(amounts) != 1 || amounts[0] != "1.2500" {
		t.Fatalf("last accepted charge not flushed: %v", amounts)
	}
}

func TestRequestDrainRejectsNewWorkAndCancelsExistingHandlers(t *testing.T) {
	tracker := newRequestDrain()
	h := server.New()
	h.Use(tracker.middleware())
	started, cancelled := make(chan struct{}), make(chan struct{})
	h.GET("/work", func(ctx context.Context, c *hertzapp.RequestContext) {
		close(started)
		<-ctx.Done()
		close(cancelled)
		c.SetStatusCode(200)
	})
	finished := make(chan struct{})
	go func() { defer close(finished); ut.PerformRequest(h.Engine, "GET", "/work", nil) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("handler did not enter")
	}
	tracker.stop()
	result := ut.PerformRequest(h.Engine, "GET", "/work", nil)
	if result.Code != 503 {
		t.Fatalf("new work admitted during shutdown: %d", result.Code)
	}
	tracker.cancelActive()
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("handler did not receive cancellation")
	}
	<-finished
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := tracker.wait(ctx); err != nil {
		t.Fatal(err)
	}
}
