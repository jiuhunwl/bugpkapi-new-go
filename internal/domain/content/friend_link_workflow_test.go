package content

import (
	"context"
	"testing"

	"bugpk-api/internal/domain/notification"
)

type friendNotificationCapture struct {
	requests []notification.EnqueueRequest
}

func (c *friendNotificationCapture) Enqueue(_ context.Context, req notification.EnqueueRequest) error {
	c.requests = append(c.requests, req)
	return nil
}

func TestNormalizeFriendLinkLanguage(t *testing.T) {
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
		if got := normalizeFriendLinkLanguage(test.input); got != test.want {
			t.Fatalf("normalizeFriendLinkLanguage(%q) = %q, want %q", test.input, got, test.want)
		}
	}
}

func TestLocalizedFriendLinkReason(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		templateKey string
		fallback    string
		language    string
		want        string
	}{
		{
			name:        "english template",
			templateKey: "backlink_missing",
			fallback:    "后台填写的中文原因",
			language:    "en",
			want:        "We could not find our link on the provided page. Please add it and submit a new application.",
		},
		{
			name:        "chinese template",
			templateKey: "site_unreachable",
			fallback:    "fallback",
			language:    "zh-CN",
			want:        "目标网站当前无法正常访问，请确认网站可公开访问后再重新申请。",
		},
		{
			name:        "custom keeps fallback",
			templateKey: "custom",
			fallback:    "自定义原因",
			language:    "en",
			want:        "自定义原因",
		},
		{
			name:        "unknown keeps fallback",
			templateKey: "unknown",
			fallback:    "fallback",
			language:    "zh",
			want:        "fallback",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := localizedFriendLinkReason(test.templateKey, test.fallback, test.language); got != test.want {
				t.Fatalf("localizedFriendLinkReason() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestNormalizeManualFriendLinkKeepsAdminPageLanguage(t *testing.T) {
	item, err := normalizeFriendLink(SaveFriendLinkRequest{
		ApplicantEmail: "applicant@example.com",
		Language:       "en-US",
		SiteName:       "Example",
		SiteURL:        "https://example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	if item.Language != "en" {
		t.Fatalf("manual friend-link language = %q, want English snapshot", item.Language)
	}
}

func TestFriendLinkSubmissionSendsApplicantReceipt(t *testing.T) {
	capture := &friendNotificationCapture{}
	service := &Service{notifier: capture}
	service.notifyFriendApplicationSubmitted(context.Background(), FriendLinkApplication{
		ChangeKind:   friendLinkChangeEdit,
		ContactEmail: "applicant@example.com",
		ID:           "9",
		Language:     "en-US",
		SiteName:     "Example",
		SiteURL:      "https://example.com",
		Username:     "alice",
	})
	if len(capture.requests) != 1 {
		t.Fatalf("notifications = %d, want applicant receipt only", len(capture.requests))
	}
	req := capture.requests[0]
	if req.TemplateKey != notification.TemplateFriendReceived {
		t.Fatalf("template = %q, want %q", req.TemplateKey, notification.TemplateFriendReceived)
	}
	if req.Recipient != "applicant@example.com" {
		t.Fatalf("recipient = %q", req.Recipient)
	}
	if req.Language != "en" {
		t.Fatalf("language = %q, want frozen English", req.Language)
	}
	if req.DedupeKey == "" {
		t.Fatal("dedupe key is empty")
	}
	if got, _ := req.Payload["changeKind"].(string); got != friendLinkChangeEdit {
		t.Fatalf("changeKind = %q, want %q", got, friendLinkChangeEdit)
	}
}

func TestFriendLinkNotificationProducerFreezesRecordLanguage(t *testing.T) {
	capture := &friendNotificationCapture{}
	service := &Service{notifier: capture}
	service.notifyFriendApplication(
		context.Background(),
		notification.TemplateFriendApproved,
		"approved",
		FriendLinkApplication{
			ContactEmail: "applicant@example.com",
			ID:           "7",
			Language:     "en-US",
			SiteName:     "Example",
			SiteURL:      "https://example.com",
		},
		"Approved",
	)
	if len(capture.requests) != 1 {
		t.Fatalf("notifications = %d, want 1", len(capture.requests))
	}
	if got := capture.requests[0].Language; got != "en" {
		t.Fatalf("application notification language = %q, want frozen English", got)
	}

	service.notifyDeletedFriendLink(
		context.Background(),
		"deleted",
		FriendLinkItem{
			ApplicantEmail: "applicant@example.com",
			ID:             "8",
			Language:       "zh-HK",
			SiteName:       "Example",
			SiteURL:        "https://example.com",
		},
		"Removed",
	)
	if len(capture.requests) != 2 {
		t.Fatalf("notifications = %d, want 2", len(capture.requests))
	}
	if got := capture.requests[1].Language; got != "zh" {
		t.Fatalf("deletion notification language = %q, want frozen Chinese", got)
	}
}
