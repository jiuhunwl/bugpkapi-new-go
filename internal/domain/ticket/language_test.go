package ticket

import (
	"context"
	"testing"

	"bugpk-api/internal/domain/notification"
)

type ticketNotificationCapture struct {
	requests []notification.EnqueueRequest
}

func (c *ticketNotificationCapture) Enqueue(_ context.Context, req notification.EnqueueRequest) error {
	c.requests = append(c.requests, req)
	return nil
}

func TestNormalizeTicketLanguage(t *testing.T) {
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
		{input: "invalid", want: "zh"},
	} {
		if got := normalizeTicketLanguage(test.input); got != test.want {
			t.Fatalf("normalizeTicketLanguage(%q) = %q, want %q", test.input, got, test.want)
		}
	}
}

func TestTicketNotificationProducerFreezesTicketLanguage(t *testing.T) {
	capture := &ticketNotificationCapture{}
	service := &Service{notifier: capture}
	service.enqueueTicketNotification(context.Background(), notification.EnqueueRequest{
		Language:    "en-US",
		Payload:     map[string]any{"ticketNo": "TK-1"},
		Recipient:   "user@example.com",
		TemplateKey: notification.TemplateTicketStatus,
	})
	if len(capture.requests) != 1 {
		t.Fatalf("notifications = %d, want 1", len(capture.requests))
	}
	if got := capture.requests[0].Language; got != "en" {
		t.Fatalf("ticket notification language = %q, want frozen English", got)
	}
}
