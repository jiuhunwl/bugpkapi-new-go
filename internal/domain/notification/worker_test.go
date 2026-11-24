package notification

import (
	"context"
	"errors"
	"testing"
	"time"

	"bugpk-api/internal/config"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

type finalizationContextRepository struct {
	loggingTestRepository
	context       context.Context
	entryError    error
	sent, retried int
}

func (r *finalizationContextRepository) capture(ctx context.Context) {
	r.context, r.entryError = ctx, ctx.Err()
}
func (r *finalizationContextRepository) MarkSent(ctx context.Context, _ int64) error {
	r.capture(ctx)
	r.sent++
	return nil
}
func (r *finalizationContextRepository) MarkRetry(ctx context.Context, _ Entry, _ error, _ time.Time, _ bool) error {
	r.capture(ctx)
	r.retried++
	return nil
}

func TestDeliveryFinalizationIsDetachedButBounded(t *testing.T) {
	for _, fail := range []bool{false, true} {
		name := "sent"
		if fail {
			name = "retry"
		}
		t.Run(name, func(t *testing.T) {
			repo := &finalizationContextRepository{loggingTestRepository: loggingTestRepository{claimEntries: []Entry{{ID: 42, Attempts: 1, MaxAttempts: 12, Language: "en", Payload: `{}`, Recipient: "recipient@example.com", TemplateKey: TemplateTicketCreated}}}}
			service := notificationLoggingTestService(zap.NewNop(), repo)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			service.sendMail = func(context.Context, config.EmailMessageConfig, string, string, string) error {
				cancel() // Shutdown can arrive just after SMTP committed delivery.
				if fail {
					return errors.New("delivery unavailable")
				}
				return nil
			}
			service.processBatch(ctx)
			if repo.context == nil {
				t.Fatal("delivery outcome was not persisted")
			}
			if repo.entryError != nil {
				t.Fatalf("finalization inherited cancellation: %v", repo.entryError)
			}
			deadline, ok := repo.context.Deadline()
			if !ok || time.Until(deadline) > 2*time.Second {
				t.Fatalf("finalization must have its own bounded deadline: %v, %v", deadline, ok)
			}
			if !errors.Is(repo.context.Err(), context.Canceled) {
				t.Fatalf("finalization resources were not released: %v", repo.context.Err())
			}
			if fail && (repo.retried != 1 || repo.sent != 0) {
				t.Fatalf("failed delivery marked sent: sent=%d retry=%d", repo.sent, repo.retried)
			}
			if !fail && (repo.sent != 1 || repo.retried != 0) {
				t.Fatalf("successful delivery marked retry: sent=%d retry=%d", repo.sent, repo.retried)
			}
		})
	}
}

type persistedFinalizationRepository struct {
	loggingTestRepository
	store *Repository
}

func (r *persistedFinalizationRepository) MarkSent(ctx context.Context, id int64) error {
	return r.store.MarkSent(ctx, id)
}
func (r *persistedFinalizationRepository) MarkRetry(ctx context.Context, entry Entry, err error, next time.Time, permanent bool) error {
	return r.store.MarkRetry(ctx, entry, err, next, permanent)
}

func TestFinalizationCannotWaitForeverForDatabaseConnection(t *testing.T) {
	for _, fail := range []bool{false, true} {
		name := "sent"
		if fail {
			name = "retry"
		}
		t.Run(name, func(t *testing.T) {
			capture := &notificationCaptureState{}
			db := openNotificationCaptureDB(t, capture)
			db.SetMaxOpenConns(1)
			held, err := db.Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer held.Close()
			repo := &persistedFinalizationRepository{store: NewRepository(db)}
			core, logs := observer.New(zap.DebugLevel)
			service := notificationLoggingTestService(zap.New(core), repo)
			done := make(chan struct{})
			go func() {
				defer close(done)
				var deliveryErr error
				if fail {
					deliveryErr = errors.New("smtp timeout token=hidden-secret")
				}
				service.finalizeDelivery(context.Background(), Entry{ID: 42, Attempts: 1, MaxAttempts: 12}, deliveryErr)
			}()
			select {
			case <-done:
			case <-time.After(4 * time.Second):
				_ = held.Close()
				<-done
				t.Fatal("finalization remained blocked waiting for a SQL connection")
			}
			if len(capture.args) != 0 {
				t.Fatal("timed-out finalization unexpectedly updated notification state")
			}
			if logs.FilterLevelExact(zap.ErrorLevel).Len() != 1 {
				t.Fatalf("expected one persistence failure, got %v", logs.All())
			}
			if db.Stats().WaitCount != 1 {
				t.Fatal("test did not exercise the database connection-pool wait")
			}
		})
	}
}

type blockingNotificationRepository struct {
	loggingTestRepository
	started chan struct{}
}

func (r *blockingNotificationRepository) Claim(ctx context.Context, _ int) ([]Entry, error) {
	close(r.started)
	<-ctx.Done()
	return nil, ctx.Err()
}
func TestNotificationWorkerCloseWaitsForCancelledClaim(t *testing.T) {
	repo := &blockingNotificationRepository{started: make(chan struct{})}
	service := notificationLoggingTestService(zap.NewNop(), repo)
	worker := service.Start(nil, time.Hour)
	defer worker.Close(context.Background())
	select {
	case <-repo.started:
	case <-time.After(time.Second):
		t.Fatal("worker did not claim")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := worker.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := worker.Close(ctx); err != nil {
		t.Fatal(err)
	}
}
