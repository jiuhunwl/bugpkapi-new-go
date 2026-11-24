package ticket

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"bugpk-api/internal/domain/notification"
	"bugpk-api/internal/pkg/emaillang"
)

const ticketNotificationTimeout = 2 * time.Second

type notificationEnqueuer interface {
	Enqueue(context.Context, notification.EnqueueRequest) error
}

type ticketNotificationTarget struct {
	Email     string
	Requester string
}

func (s *Service) SetNotifier(notifier notificationEnqueuer) {
	if s != nil {
		s.notifier = notifier
	}
}

func (s *Service) notifyTicketAdmins(
	ctx context.Context,
	templateKey string,
	dedupeEvent string,
	ticket Ticket,
	preview string,
) {
	if s == nil || s.notifier == nil {
		return
	}
	emails, err := s.repo.AdminNotificationEmails(ctx)
	if err != nil {
		return
	}
	payload := ticketNotificationPayload(ticket, preview)
	for _, email := range emails {
		s.enqueueTicketNotification(ctx, notification.EnqueueRequest{
			DedupeKey:   ticketNotificationDedupe(ticket.ID, dedupeEvent, email),
			Language:    ticket.Language,
			Payload:     payload,
			Recipient:   email,
			TemplateKey: templateKey,
		})
	}
}

func (s *Service) notifyTicketOwner(
	ctx context.Context,
	templateKey string,
	dedupeEvent string,
	ticket Ticket,
	preview string,
) {
	if s == nil || s.notifier == nil {
		return
	}
	target, err := s.repo.TicketNotificationTarget(ctx, parseTicketID(ticket.ID))
	if err != nil || strings.TrimSpace(target.Email) == "" {
		return
	}
	payload := ticketNotificationPayload(ticket, preview)
	if strings.TrimSpace(payload["requester"].(string)) == "" {
		payload["requester"] = target.Requester
	}
	s.enqueueTicketNotification(ctx, notification.EnqueueRequest{
		DedupeKey:   ticketNotificationDedupe(ticket.ID, dedupeEvent, target.Email),
		Language:    ticket.Language,
		Payload:     payload,
		Recipient:   target.Email,
		TemplateKey: templateKey,
	})
}

func (s *Service) enqueueTicketNotification(
	ctx context.Context,
	req notification.EnqueueRequest,
) {
	// Ticket language is a creation-time snapshot. Normalize it before handing
	// the request to the outbox so alternative notifier implementations cannot
	// accidentally re-evaluate a browser or site language later.
	req.Language = emaillang.Resolve(req.Language, "", "", "")
	enqueueCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), ticketNotificationTimeout)
	defer cancel()
	_ = s.notifier.Enqueue(enqueueCtx, req)
}

func ticketNotificationPayload(ticket Ticket, preview string) map[string]any {
	requester := strings.TrimSpace(ticket.DisplayName)
	if requester == "" {
		requester = strings.TrimSpace(ticket.Username)
	}
	if requester == "" && ticket.UserID != "" {
		requester = "#" + ticket.UserID
	}
	return map[string]any{
		"category":  ticket.Category,
		"preview":   messagePreview(preview),
		"priority":  ticket.Priority,
		"requester": requester,
		"status":    ticket.Status,
		"subject":   ticket.Subject,
		"ticketNo":  ticket.TicketNo,
	}
}

func ticketNotificationDedupe(ticketID, event, recipient string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(recipient))))
	return fmt.Sprintf(
		"ticket:%s:%s:%s",
		strings.TrimSpace(ticketID),
		strings.TrimSpace(event),
		hex.EncodeToString(sum[:8]),
	)
}

func parseTicketID(value string) int64 {
	var result int64
	_, _ = fmt.Sscan(strings.TrimSpace(value), &result)
	return result
}

func (r *Repository) TicketNotificationTarget(
	ctx context.Context,
	ticketID int64,
) (ticketNotificationTarget, error) {
	var target ticketNotificationTarget
	err := r.db.QueryRowContext(ctx, `
		select coalesce(u.email, ''), coalesce(nullif(u.display_name, ''), u.username)
		from bp_ticket t
		join bp_user u on u.id=t.user_id
		where t.id=?
	`, ticketID).Scan(&target.Email, &target.Requester)
	if err != nil {
		return ticketNotificationTarget{}, err
	}
	target.Email = strings.ToLower(strings.TrimSpace(target.Email))
	target.Requester = strings.TrimSpace(target.Requester)
	return target, nil
}

func (r *Repository) AdminNotificationEmails(ctx context.Context) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, `
		select distinct lower(trim(u.email))
		from bp_user u
		join bp_user_role ur on ur.user_id=u.id
		join bp_role r on r.id=ur.role_id and r.role_key='admin' and r.status=1
		where u.status=1 and u.email is not null and trim(u.email)<>''
		order by lower(trim(u.email))
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	emails := make([]string, 0, 4)
	for rows.Next() {
		var email string
		if err := rows.Scan(&email); err != nil {
			return nil, err
		}
		if email = strings.TrimSpace(email); email != "" {
			emails = append(emails, email)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return emails, nil
}
