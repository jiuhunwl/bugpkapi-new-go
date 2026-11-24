package notification

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"bugpk-api/internal/config"

	"go.uber.org/zap"
)

type outboxRepositoryStub struct {
	enqueued EnqueueRequest
}

func (s *outboxRepositoryStub) Enqueue(_ context.Context, req EnqueueRequest) error {
	s.enqueued = req
	return nil
}

func (s *outboxRepositoryStub) Claim(context.Context, int) ([]Entry, error) {
	return nil, nil
}

func (s *outboxRepositoryStub) MarkRetry(context.Context, Entry, error, time.Time, bool) error {
	return nil
}

func (s *outboxRepositoryStub) MarkSent(context.Context, int64) error { return nil }

type retryOutboxRepositoryStub struct {
	entry       Entry
	ready       bool
	retryCount  int
	sentCount   int
	retryEntry  Entry
	sentEntryID int64
}

func (s *retryOutboxRepositoryStub) Enqueue(_ context.Context, req EnqueueRequest) error {
	payload, err := json.Marshal(req.Payload)
	if err != nil {
		return err
	}
	s.entry = Entry{
		ID:          1,
		Language:    req.Language,
		MaxAttempts: 3,
		Payload:     string(payload),
		Recipient:   req.Recipient,
		TemplateKey: req.TemplateKey,
	}
	s.ready = true
	return nil
}

func (s *retryOutboxRepositoryStub) Claim(context.Context, int) ([]Entry, error) {
	if !s.ready {
		return nil, nil
	}
	s.ready = false
	s.entry.Attempts++
	return []Entry{s.entry}, nil
}

func (s *retryOutboxRepositoryStub) MarkRetry(
	_ context.Context,
	entry Entry,
	_ error,
	_ time.Time,
	_ bool,
) error {
	s.retryCount++
	s.retryEntry = entry
	s.entry = entry
	s.ready = true
	return nil
}

func (s *retryOutboxRepositoryStub) MarkSent(_ context.Context, id int64) error {
	s.sentCount++
	s.sentEntryID = id
	return nil
}

func TestRenderTicketReplyUsesLanguageSnapshot(t *testing.T) {
	payload := map[string]any{
		"preview":  "The request is now fixed.",
		"subject":  "Gateway error",
		"ticketNo": "TK20260811-ABC",
	}
	english, err := Render(TemplateTicketStaffReply, "en-US", "Acme API", payload)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(english.Subject, "New reply") || !strings.Contains(english.Body, "Support replied") {
		t.Fatalf("unexpected English message: %+v", english)
	}
	chinese, err := Render(TemplateTicketStaffReply, "zh-CN", "Acme API", payload)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(chinese.Subject, "收到新回复") || !strings.Contains(chinese.Body, "支持人员回复") {
		t.Fatalf("unexpected Chinese message: %+v", chinese)
	}
}

func TestRenderFriendLinkSubmissionTemplates(t *testing.T) {
	payload := map[string]any{
		"applicant":    "alice",
		"changeKind":   "EDIT",
		"contactEmail": "applicant@example.com",
		"siteName":     "Example",
		"siteUrl":      "https://example.com",
	}
	received, err := Render(TemplateFriendReceived, "zh-CN", "Acme API", payload)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(received.Subject, "已收到") || !strings.Contains(received.Body, "友链信息修改请求") {
		t.Fatalf("unexpected receipt message: %+v", received)
	}
	pending, err := Render(TemplateFriendPending, "en-US", "Acme API", payload)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(pending.Subject, "awaiting review") ||
		!strings.Contains(pending.Body, "friend link change request") ||
		!strings.Contains(pending.Body, "applicant@example.com") {
		t.Fatalf("unexpected pending review message: %+v", pending)
	}
}

func TestEnqueueFreezesNormalizedLanguageSnapshot(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		input string
		want  string
	}{
		{input: "zh-CN", want: "zh"},
		{input: "zh-TW", want: "zh"},
		{input: "zh-HK", want: "zh"},
		{input: "zh-Hant-HK", want: "zh"},
		{input: "en-US", want: "en"},
		{input: "", want: "zh"},
		{input: "illegal", want: "zh"},
	} {
		repo := &outboxRepositoryStub{}
		service := &Service{repo: repo}
		err := service.Enqueue(context.Background(), EnqueueRequest{
			Language:    test.input,
			Payload:     map[string]any{"ticketNo": "TK-1"},
			Recipient:   "User@Example.com",
			TemplateKey: TemplateTicketStatus,
		})
		if err != nil {
			t.Fatal(err)
		}
		if repo.enqueued.Language != test.want {
			t.Fatalf("outbox language for %q = %q, want %q", test.input, repo.enqueued.Language, test.want)
		}
		if repo.enqueued.Recipient != "user@example.com" {
			t.Fatalf("recipient = %q", repo.enqueued.Recipient)
		}
		if repo.enqueued.DedupeKey == "" {
			t.Fatal(errors.New("dedupe key was not generated"))
		}
	}
}

func TestOutboxRetryKeepsFrozenLanguageSnapshot(t *testing.T) {
	repo := &retryOutboxRepositoryStub{}
	service := &Service{
		fallback: config.EmailMessageConfig{
			Enabled:     true,
			Provider:    "smtp",
			SMTPTimeout: time.Second,
		},
		logger: zap.NewNop(),
		repo:   repo,
	}

	type delivery struct {
		body    string
		subject string
	}
	deliveries := make([]delivery, 0, 2)
	service.sendMail = func(
		context.Context,
		config.EmailMessageConfig,
		string,
		string,
		string,
	) error {
		panic("unreachable")
	}
	attempt := 0
	service.sendMail = func(
		_ context.Context,
		_ config.EmailMessageConfig,
		_ string,
		subject string,
		body string,
	) error {
		attempt++
		deliveries = append(deliveries, delivery{body: body, subject: subject})
		if attempt == 1 {
			return errors.New("temporary SMTP failure")
		}
		return nil
	}

	err := service.Enqueue(context.Background(), EnqueueRequest{
		Language:    "en-US",
		Payload:     map[string]any{"preview": "Fixed", "subject": "Gateway", "ticketNo": "TK-1"},
		Recipient:   "user@example.com",
		TemplateKey: TemplateTicketStaffReply,
	})
	if err != nil {
		t.Fatal(err)
	}
	if repo.entry.Language != "en" {
		t.Fatalf("stored language = %q, want frozen English snapshot", repo.entry.Language)
	}

	service.processBatch(context.Background())
	service.processBatch(context.Background())

	if repo.retryCount != 1 || repo.sentCount != 1 || repo.sentEntryID != repo.entry.ID {
		t.Fatalf(
			"retry/sent state = retry:%d sent:%d id:%d",
			repo.retryCount,
			repo.sentCount,
			repo.sentEntryID,
		)
	}
	if repo.retryEntry.Language != "en" || repo.entry.Language != "en" {
		t.Fatalf(
			"language changed across retry: retry=%q current=%q",
			repo.retryEntry.Language,
			repo.entry.Language,
		)
	}
	if len(deliveries) != 2 {
		t.Fatalf("delivery attempts = %d, want 2", len(deliveries))
	}
	for index, message := range deliveries {
		if !strings.Contains(message.subject, "New reply") || !strings.Contains(message.body, "Support replied") {
			t.Fatalf("attempt %d changed language: %+v", index+1, message)
		}
	}
}

func TestRenderRejectsUnknownTemplatePermanently(t *testing.T) {
	_, err := Render("unknown", "zh", "BugPk-Api", nil)
	if err == nil || !isPermanent(err) {
		t.Fatalf("error = %v, want permanent template error", err)
	}
}

func TestNextAttemptDelayIsBounded(t *testing.T) {
	if got := nextAttemptDelay(1); got != time.Minute {
		t.Fatalf("first delay = %s", got)
	}
	if got := nextAttemptDelay(20); got != 6*time.Hour {
		t.Fatalf("bounded delay = %s", got)
	}
}

func TestMergeEmailConfigKeepsRuntimeDefaults(t *testing.T) {
	base := config.EmailMessageConfig{
		Provider:     "smtp",
		SMTPPort:     587,
		SMTPTimeout:  12 * time.Second,
		SMTPFromName: "BugPk-Api",
	}
	override := config.EmailMessageConfig{Enabled: true, SMTPHost: "smtp.example.com"}
	got := mergeEmailConfig(base, override)
	if got.SMTPPort != 587 || got.SMTPTimeout != 12*time.Second || got.Provider != "smtp" {
		t.Fatalf("merged config = %+v", got)
	}
}
