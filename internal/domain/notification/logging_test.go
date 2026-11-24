package notification

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"bugpk-api/internal/config"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

type loggingTestRepository struct {
	claimEntries []Entry
	claimErr     error
	enqueueErr   error
	markRetryErr error
	markSentErr  error
}

func (r *loggingTestRepository) Claim(context.Context, int) ([]Entry, error) {
	return r.claimEntries, r.claimErr
}

func (r *loggingTestRepository) Enqueue(context.Context, EnqueueRequest) error {
	return r.enqueueErr
}

func (r *loggingTestRepository) MarkRetry(context.Context, Entry, error, time.Time, bool) error {
	return r.markRetryErr
}

func (r *loggingTestRepository) MarkSent(context.Context, int64) error {
	return r.markSentErr
}

func TestEnqueueMasksRepositoryErrorInLog(t *testing.T) {
	core, logs := observer.New(zap.ErrorLevel)
	service := &Service{
		logger: zap.New(core),
		repo: &loggingTestRepository{
			enqueueErr: errors.New("insert failed password=enqueue-secret for alice@example.com"),
		},
	}

	err := service.Enqueue(context.Background(), EnqueueRequest{
		Recipient:   "recipient@example.com",
		TemplateKey: TemplateTicketCreated,
	})
	if err == nil {
		t.Fatal("Enqueue() error = nil")
	}
	assertMaskedNotificationLogErrors(t, logs, 1, "enqueue-secret", "alice@example.com")
}

func TestProcessBatchMasksClaimErrorInLog(t *testing.T) {
	core, logs := observer.New(zap.ErrorLevel)
	service := &Service{
		logger: zap.New(core),
		repo: &loggingTestRepository{
			claimErr: errors.New("claim failed token=claim-secret for alice@example.com"),
		},
	}

	service.processBatch(context.Background())

	assertMaskedNotificationLogErrors(t, logs, 1, "claim-secret", "alice@example.com")
}

func TestProcessBatchMasksDeliveryAndRetryErrorsInLogs(t *testing.T) {
	core, logs := observer.New(zap.DebugLevel)
	service := notificationLoggingTestService(zap.New(core), &loggingTestRepository{
		claimEntries: []Entry{{
			Attempts:    1,
			ID:          42,
			Language:    "zh",
			MaxAttempts: 12,
			Payload:     `{}`,
			Recipient:   "recipient@example.com",
			TemplateKey: TemplateTicketCreated,
		}},
		markRetryErr: errors.New("retry failed password=retry-secret for retry@example.com"),
	})
	service.sendMail = func(context.Context, config.EmailMessageConfig, string, string, string) error {
		return errors.New("smtp failed token=delivery-secret for delivery@example.com")
	}

	service.processBatch(context.Background())

	assertMaskedNotificationLogErrors(
		t,
		logs,
		2,
		"retry-secret",
		"retry@example.com",
		"delivery-secret",
		"delivery@example.com",
	)
}

func TestProcessBatchMasksMarkSentErrorInLog(t *testing.T) {
	core, logs := observer.New(zap.ErrorLevel)
	service := notificationLoggingTestService(zap.New(core), &loggingTestRepository{
		claimEntries: []Entry{{
			Attempts:    1,
			ID:          43,
			Language:    "en",
			MaxAttempts: 12,
			Payload:     `{}`,
			Recipient:   "recipient@example.com",
			TemplateKey: TemplateTicketCreated,
		}},
		markSentErr: errors.New("update failed api_key=sent-secret for sent@example.com"),
	})
	service.sendMail = func(context.Context, config.EmailMessageConfig, string, string, string) error {
		return nil
	}

	service.processBatch(context.Background())

	assertMaskedNotificationLogErrors(t, logs, 1, "sent-secret", "sent@example.com")
}

func notificationLoggingTestService(logger *zap.Logger, repo outboxRepository) *Service {
	return &Service{
		fallback: config.EmailMessageConfig{
			Enabled:  true,
			Provider: "smtp",
		},
		logger: logger,
		repo:   repo,
	}
}

func assertMaskedNotificationLogErrors(
	t *testing.T,
	logs *observer.ObservedLogs,
	wantCount int,
	forbidden ...string,
) {
	t.Helper()
	entries := logs.All()
	if len(entries) != wantCount {
		t.Fatalf("notification log count = %d, want %d: %+v", len(entries), wantCount, entries)
	}
	for _, entry := range entries {
		loggedError := fmt.Sprint(entry.ContextMap()["error"])
		if !strings.Contains(loggedError, "[REDACTED]") {
			t.Fatalf("notification error log was not redacted: %q", loggedError)
		}
		for _, secret := range forbidden {
			if strings.Contains(loggedError, secret) {
				t.Fatalf("notification error log leaked %q: %q", secret, loggedError)
			}
		}
	}
}
