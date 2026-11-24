package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"bugpk-api/internal/config"

	"go.uber.org/zap"
)

func TestPasswordResetResponseFloor(t *testing.T) {
	service := &Service{passwordResetResponseFloor: 35 * time.Millisecond}
	startedAt := time.Now()
	service.waitForPasswordResetResponse(context.Background(), startedAt)
	if elapsed := time.Since(startedAt); elapsed < 30*time.Millisecond {
		t.Fatalf("password reset response floor elapsed = %s, want at least 30ms", elapsed)
	}
}

func TestPasswordResetResponseFloorHonorsCancellation(t *testing.T) {
	service := &Service{passwordResetResponseFloor: time.Second}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	startedAt := time.Now()
	service.waitForPasswordResetResponse(ctx, startedAt)
	if elapsed := time.Since(startedAt); elapsed > 100*time.Millisecond {
		t.Fatalf("canceled password reset response waited too long: %s", elapsed)
	}
}

func TestPasswordResetDeliveryTimeoutBounds(t *testing.T) {
	if got := passwordResetDeliveryTimeout(0); got != passwordResetDeliveryMinTimeout {
		t.Fatalf("zero SMTP timeout = %s, want %s", got, passwordResetDeliveryMinTimeout)
	}
	if got := passwordResetDeliveryTimeout(10 * time.Second); got != 12*time.Second {
		t.Fatalf("normal SMTP timeout = %s, want 12s", got)
	}
	if got := passwordResetDeliveryTimeout(10 * time.Minute); got != passwordResetDeliveryMaxTimeout {
		t.Fatalf("large SMTP timeout = %s, want %s", got, passwordResetDeliveryMaxTimeout)
	}
}

func TestPasswordResetDeliverySlotsAreBounded(t *testing.T) {
	service := &Service{passwordResetSlots: make(chan struct{}, 1)}
	if !service.acquirePasswordResetSlot() {
		t.Fatal("first delivery slot was rejected")
	}
	if service.acquirePasswordResetSlot() {
		t.Fatal("delivery slot limit was not enforced")
	}
	service.releasePasswordResetSlot()
	if !service.acquirePasswordResetSlot() {
		t.Fatal("released delivery slot was not reusable")
	}
	service.releasePasswordResetSlot()
}

func TestPasswordResetBackgroundLifecycleWaitsAndRejectsNewTasks(t *testing.T) {
	service := &Service{log: zap.NewNop()}
	started := make(chan struct{})
	release := make(chan struct{})
	if !service.startPasswordResetBackground(func() {
		close(started)
		<-release
	}) {
		t.Fatal("initial password reset background task was rejected")
	}
	<-started

	shortCtx, cancelShort := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancelShort()
	if err := service.Close(shortCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Close() error = %v, want deadline exceeded", err)
	}
	sharedDone := service.backgroundDone
	if sharedDone == nil {
		t.Fatal("Close() did not initialize the shared background completion channel")
	}
	secondCtx, cancelSecond := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancelSecond()
	if err := service.Close(secondCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second Close() error = %v, want deadline exceeded", err)
	}
	if service.backgroundDone != sharedDone {
		t.Fatal("repeated Close() replaced the shared background completion channel")
	}
	if service.startPasswordResetBackground(func() {}) {
		t.Fatal("password reset task was accepted after shutdown started")
	}

	close(release)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := service.Close(ctx); err != nil {
		t.Fatalf("Close() after task release = %v", err)
	}
}

func TestPasswordResetBackgroundPanicDoesNotBlockShutdown(t *testing.T) {
	service := &Service{log: zap.NewNop()}
	if !service.startPasswordResetBackground(func() { panic("test panic") }) {
		t.Fatal("password reset panic task was rejected")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := service.Close(ctx); err != nil {
		t.Fatalf("panic task blocked shutdown: %v", err)
	}
}

func TestPasswordResetCloseCancelsDeliveryContext(t *testing.T) {
	service := NewService(config.AuthConfig{}, nil, nil, nil, nil, nil, nil)
	started := make(chan struct{})
	canceled := make(chan struct{})
	if !service.startPasswordResetBackground(func() {
		close(started)
		<-service.passwordResetBackgroundContext().Done()
		close(canceled)
	}) {
		t.Fatal("password reset cancellation task was rejected")
	}
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := service.Close(ctx); err != nil {
		t.Fatalf("Close() did not cancel delivery context: %v", err)
	}
	select {
	case <-canceled:
	default:
		t.Fatal("delivery context was not canceled before Close returned")
	}
}
