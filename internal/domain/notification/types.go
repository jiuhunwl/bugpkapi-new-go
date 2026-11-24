package notification

import "time"

const (
	ChannelEmail = "EMAIL"

	StatusPending    = "PENDING"
	StatusProcessing = "PROCESSING"
	StatusRetry      = "RETRY"
	StatusSent       = "SENT"
	StatusFailed     = "FAILED"

	TemplateTicketCreated    = "ticket.created"
	TemplateTicketUserReply  = "ticket.user_reply"
	TemplateTicketUserClosed = "ticket.user_closed"
	TemplateTicketStaffReply = "ticket.staff_reply"
	TemplateTicketStatus     = "ticket.status"
	TemplateFriendReceived   = "friend_link.received"
	TemplateFriendPending    = "friend_link.pending_review"
	TemplateFriendApproved   = "friend_link.approved"
	TemplateFriendRejected   = "friend_link.rejected"
	TemplateFriendDeleted    = "friend_link.deleted"
	TemplatePaymentPaid      = "payment.paid"
	TemplateBroadcast        = "inbox.broadcast"
)

type EnqueueRequest struct {
	DedupeKey   string
	Language    string
	Payload     map[string]any
	Recipient   string
	TemplateKey string
}

type Entry struct {
	Attempts    int
	ID          int64
	Language    string
	MaxAttempts int
	Payload     string
	Recipient   string
	TemplateKey string
}

type RenderedMessage struct {
	Body    string
	Subject string
}

func nextAttemptDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	shift := attempt - 1
	if shift > 9 {
		shift = 9
	}
	delay := time.Minute * time.Duration(1<<shift)
	if delay > 6*time.Hour {
		return 6 * time.Hour
	}
	return delay
}
