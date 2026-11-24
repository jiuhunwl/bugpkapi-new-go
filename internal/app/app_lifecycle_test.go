package app

import (
	"context"
	"errors"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"bugpk-api/internal/config"

	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/network/standard"
)

func TestApplicationRunReturnsListenError(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	port := occupied.Addr().(*net.TCPAddr).Port
	cfg := lifecycleServerConfig(port)
	application := &Application{
		Config: config.Config{Server: cfg},
		Server: newHTTPServer(cfg),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err = application.Run(ctx)
	if err == nil || !strings.Contains(err.Error(), "http server stopped") {
		t.Fatalf("Run() error=%v, want listen failure", err)
	}
	if !strings.Contains(err.Error(), occupied.Addr().String()) {
		t.Fatalf("Run() lost the failed listen address: %v", err)
	}
	if application.Server.IsRunning() {
		t.Fatal("server remains running after a failed bind")
	}
}

func TestRunHTTPServerPreservesUnexpectedPanics(t *testing.T) {
	for _, message := range []string{"unexpected startup failure", "create netpoll event-loop fail", "netpoll server exit"} {
		t.Run(message, func(t *testing.T) {
			h := newHTTPServer(lifecycleServerConfig(0))
			h.OnRun = append(h.OnRun, func(context.Context) error { panic(message) })
			defer func() {
				if recovered := recover(); recovered != message {
					t.Fatalf("panic = %v, want %q", recovered, message)
				}
			}()
			_ = runHTTPServer(h)
		})
	}
	t.Run("standard transport", func(t *testing.T) {
		const message = "create netpoll listener fail: unrelated panic"
		h := server.New(server.WithTransport(standard.NewTransporter))
		h.OnRun = append(h.OnRun, func(context.Context) error { panic(message) })
		defer func() {
			if recovered := recover(); recovered != message {
				t.Fatalf("panic = %v, want %q", recovered, message)
			}
		}()
		_ = runHTTPServer(h)
	})
}

func TestApplicationRunShutsDownOnContextCancellation(t *testing.T) {
	port := reserveTCPPort(t)
	cfg := lifecycleServerConfig(port)
	application := &Application{
		Config: config.Config{Server: cfg},
		Server: newHTTPServer(cfg),
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- application.Run(ctx)
	}()

	address := net.JoinHostPort(cfg.Host, strconv.Itoa(port))
	deadline := time.Now().Add(3 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", address, 100*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("server did not start listening on %s: %v", address, err)
		}
		time.Sleep(20 * time.Millisecond)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("graceful Run() returned error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("graceful shutdown did not finish")
	}
}

func TestApplicationCloseIsConcurrentSafe(t *testing.T) {
	var closeCalls atomic.Int32
	application := &Application{
		backgroundClosers: []func(context.Context) error{
			func(context.Context) error {
				closeCalls.Add(1)
				return nil
			},
		},
	}
	const callers = 20
	var wait sync.WaitGroup
	wait.Add(callers)
	for range callers {
		go func() {
			defer wait.Done()
			_ = application.Close()
		}()
	}
	wait.Wait()
	if err := application.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if got := closeCalls.Load(); got != 1 {
		t.Fatalf("background closer calls = %d, want 1", got)
	}
}

func TestApplicationCloseAggregatesAndCachesCloserErrors(t *testing.T) {
	firstErr := context.DeadlineExceeded
	secondErr := context.Canceled
	var calls atomic.Int32
	application := &Application{
		backgroundClosers: []func(context.Context) error{
			func(context.Context) error {
				calls.Add(1)
				return firstErr
			},
			func(context.Context) error {
				calls.Add(1)
				return secondErr
			},
		},
	}
	err := application.Close()
	if !errors.Is(err, firstErr) || !errors.Is(err, secondErr) {
		t.Fatalf("Close() error = %v, want both closer errors", err)
	}
	if cached := application.Close(); cached != err {
		t.Fatalf("Close() cached error changed: first=%v second=%v", err, cached)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("closer calls = %d, want 2", got)
	}
}

func lifecycleServerConfig(port int) config.ServerConfig {
	cfg := config.Default().Server
	cfg.Host = "127.0.0.1"
	cfg.Port = port
	cfg.ShutdownTimeout = time.Second
	return cfg
}

func reserveTCPPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}
