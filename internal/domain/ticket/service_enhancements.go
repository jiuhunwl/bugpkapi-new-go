package ticket

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"bugpk-api/internal/domain/notification"
	"bugpk-api/internal/pkg/emaillang"
	apperrors "bugpk-api/internal/pkg/errors"

	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

const (
	maxTicketTags          = 8
	maxTicketTagRunes      = 24
	maxQuickReplyTitle     = 80
	maxQuickReplyContent   = 4000
	maxQuickReplySortOrder = 10_000
)

func (s *Service) AddInternalNote(ctx context.Context, principal Principal, ticketID int64, req InternalNoteRequest) (Detail, error) {
	if err := s.authz.Require(ctx, principal, "ticket:admin:note"); err != nil {
		return Detail{}, err
	}
	attachmentIDs, err := normalizeAttachmentIDs(req.AttachmentIDs)
	if err != nil {
		return Detail{}, err
	}
	content, err := normalizeReplyContent(req.Content, len(attachmentIDs) > 0)
	if err != nil {
		return Detail{}, err
	}
	ownerID, messageID, err := s.repo.AddInternalNote(ctx, ticketID, principal.ID, content, attachmentIDs...)
	if err != nil {
		return Detail{}, mapTicketError(err)
	}
	result, err := s.repo.Detail(ctx, ticketID, principal.ID, true)
	if err != nil {
		return Detail{}, mapTicketError(err)
	}
	s.publish(ctx, Event{
		ActorID:   strconv.FormatInt(principal.ID, 10),
		MessageID: strconv.FormatInt(messageID, 10),
		TicketID:  strconv.FormatInt(ticketID, 10),
		Type:      "ticket.note",
		UserID:    strconv.FormatInt(ownerID, 10),
	})
	return result, nil
}

func (s *Service) Close(ctx context.Context, principal Principal, ticketID int64) (Detail, error) {
	admin, err := s.isAdmin(ctx, principal)
	if err != nil {
		return Detail{}, err
	}
	if admin {
		if err := s.authz.Require(ctx, principal, "ticket:admin:update"); err != nil {
			return Detail{}, err
		}
		status := StatusClosed
		return s.Update(ctx, principal, ticketID, UpdateRequest{Status: &status})
	}
	if err := s.authz.Require(ctx, principal, "ticket:user:close"); err != nil {
		return Detail{}, err
	}
	if err := s.repo.CloseByOwner(ctx, ticketID, principal.ID); err != nil {
		return Detail{}, mapTicketError(err)
	}
	result, err := s.repo.Detail(ctx, ticketID, principal.ID, false)
	if err != nil {
		return Detail{}, mapTicketError(err)
	}
	s.publish(ctx, Event{
		ActorID:  strconv.FormatInt(principal.ID, 10),
		TicketID: strconv.FormatInt(ticketID, 10),
		Type:     "ticket.closed",
		UserID:   strconv.FormatInt(principal.ID, 10),
	})
	s.notifyTicketAdmins(
		ctx,
		notification.TemplateTicketUserClosed,
		"user-closed:"+result.Ticket.UpdatedAt,
		result.Ticket,
		"",
	)
	return result, nil
}

func (s *Service) Statistics(ctx context.Context, principal Principal, days int) (Statistics, error) {
	if err := s.authz.Require(ctx, principal, "ticket:admin:stats"); err != nil {
		return Statistics{}, err
	}
	if days == 0 {
		days = 7
	}
	if days != 7 && days != 30 {
		return Statistics{}, apperrors.New(consts.StatusBadRequest, "statistics range must be 7 or 30 days")
	}
	return s.repo.Statistics(ctx, days)
}

func (s *Service) QuickReplies(ctx context.Context, principal Principal) ([]QuickReply, error) {
	if err := s.authz.Require(ctx, principal, "ticket:admin:quick-reply"); err != nil {
		return nil, err
	}
	return s.repo.QuickReplies(ctx)
}

func (s *Service) CreateQuickReply(ctx context.Context, principal Principal, req SaveQuickReplyRequest) (QuickReply, error) {
	if err := s.authz.Require(ctx, principal, "ticket:admin:quick-reply"); err != nil {
		return QuickReply{}, err
	}
	normalized, err := normalizeQuickReply(req)
	if err != nil {
		return QuickReply{}, err
	}
	return s.repo.CreateQuickReply(ctx, principal.ID, normalized)
}

func (s *Service) UpdateQuickReply(ctx context.Context, principal Principal, id int64, req SaveQuickReplyRequest) (QuickReply, error) {
	if err := s.authz.Require(ctx, principal, "ticket:admin:quick-reply"); err != nil {
		return QuickReply{}, err
	}
	normalized, err := normalizeQuickReply(req)
	if err != nil {
		return QuickReply{}, err
	}
	result, err := s.repo.UpdateQuickReply(ctx, id, normalized)
	return result, mapTicketError(err)
}

func (s *Service) DeleteQuickReply(ctx context.Context, principal Principal, id int64) error {
	if err := s.authz.Require(ctx, principal, "ticket:admin:quick-reply"); err != nil {
		return err
	}
	return mapTicketError(s.repo.DeleteQuickReply(ctx, id))
}

func (s *Service) AddAttachment(ctx context.Context, principal Principal, ticketID int64, file AttachmentFile) (Attachment, error) {
	admin, err := s.isAdmin(ctx, principal)
	if err != nil {
		return Attachment{}, err
	}
	permission := "ticket:user:attachment"
	if admin {
		permission = "ticket:admin:attachment"
	}
	if err := s.authz.Require(ctx, principal, permission); err != nil {
		return Attachment{}, err
	}
	if strings.TrimSpace(file.Storage) == "" {
		file.Storage = AttachmentStorageLocal
	}
	result, err := s.repo.AddAttachment(ctx, ticketID, principal.ID, file, admin)
	if err != nil {
		return Attachment{}, mapTicketError(err)
	}
	s.publish(ctx, Event{
		ActorID:  strconv.FormatInt(principal.ID, 10),
		TicketID: strconv.FormatInt(ticketID, 10),
		Type:     "ticket.attachment",
		UserID:   attachmentOwnerID(ctx, s.repo, ticketID, principal.ID, admin),
	})
	return result, nil
}

func (s *Service) AuthorizeAttachmentUpload(ctx context.Context, principal Principal, ticketID int64) error {
	admin, err := s.isAdmin(ctx, principal)
	if err != nil {
		return err
	}
	permission := "ticket:user:attachment"
	if admin {
		permission = "ticket:admin:attachment"
	}
	if err := s.authz.Require(ctx, principal, permission); err != nil {
		return err
	}
	_, err = s.repo.CanView(ctx, ticketID, principal.ID, admin)
	return mapTicketError(err)
}

func (s *Service) Attachment(ctx context.Context, principal Principal, ticketID, attachmentID int64) (AttachmentFile, error) {
	admin, err := s.isAdmin(ctx, principal)
	if err != nil {
		return AttachmentFile{}, err
	}
	file, err := s.repo.AttachmentForDownload(ctx, ticketID, attachmentID, principal.ID, admin)
	return file, mapTicketError(err)
}

func (s *Service) DeleteAttachment(ctx context.Context, principal Principal, ticketID, attachmentID int64) (string, error) {
	admin, err := s.isAdmin(ctx, principal)
	if err != nil {
		return "", err
	}
	permission := "ticket:user:attachment"
	if admin {
		permission = "ticket:admin:attachment"
	}
	if err := s.authz.Require(ctx, principal, permission); err != nil {
		return "", err
	}
	storedName, err := s.repo.DeleteAttachment(ctx, ticketID, attachmentID, principal.ID, admin)
	if err != nil {
		return "", mapTicketError(err)
	}
	s.publish(ctx, Event{
		ActorID:  strconv.FormatInt(principal.ID, 10),
		TicketID: strconv.FormatInt(ticketID, 10),
		Type:     "ticket.attachment",
		UserID:   attachmentOwnerID(ctx, s.repo, ticketID, principal.ID, admin),
	})
	return storedName, nil
}

func attachmentOwnerID(ctx context.Context, repo *Repository, ticketID, fallbackID int64, admin bool) string {
	ownerID, err := repo.CanView(ctx, ticketID, fallbackID, admin)
	if err != nil || ownerID <= 0 {
		ownerID = fallbackID
	}
	return strconv.FormatInt(ownerID, 10)
}

func normalizeTicketLanguage(value string) string {
	return emaillang.Resolve(value, "", "", "")
}

func normalizeTicketTags(values []string) ([]string, error) {
	if len(values) > maxTicketTags {
		return nil, apperrors.New(consts.StatusBadRequest, "ticket tags must not exceed 8")
	}
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || utf8.RuneCountInString(value) > maxTicketTagRunes || strings.ContainsRune(value, '\x00') {
			return nil, apperrors.New(consts.StatusBadRequest, "ticket tag is invalid")
		}
		key := strings.ToLower(value)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, value)
	}
	sort.SliceStable(result, func(i, j int) bool {
		return strings.ToLower(result[i]) < strings.ToLower(result[j])
	})
	return result, nil
}

func normalizeQuickReply(req SaveQuickReplyRequest) (SaveQuickReplyRequest, error) {
	req.Title = strings.TrimSpace(req.Title)
	req.ContentZH = strings.TrimSpace(req.ContentZH)
	req.ContentEN = strings.TrimSpace(req.ContentEN)
	req.Category = strings.ToUpper(strings.TrimSpace(req.Category))
	if req.Category == "" {
		req.Category = CategoryGeneral
	}
	if req.Title == "" || utf8.RuneCountInString(req.Title) > maxQuickReplyTitle || strings.ContainsRune(req.Title, '\x00') {
		return SaveQuickReplyRequest{}, apperrors.New(consts.StatusBadRequest, "quick reply title is invalid")
	}
	if req.ContentZH == "" || req.ContentEN == "" || utf8.RuneCountInString(req.ContentZH) > maxQuickReplyContent || utf8.RuneCountInString(req.ContentEN) > maxQuickReplyContent {
		return SaveQuickReplyRequest{}, apperrors.New(consts.StatusBadRequest, "quick reply content is invalid")
	}
	if !validTicketCategory(req.Category) {
		return SaveQuickReplyRequest{}, apperrors.New(consts.StatusBadRequest, "quick reply category is invalid")
	}
	if req.SortNo < 0 || req.SortNo > maxQuickReplySortOrder {
		return SaveQuickReplyRequest{}, apperrors.New(consts.StatusBadRequest, "quick reply sort order is invalid")
	}
	status := 1
	if req.Status != nil {
		status = *req.Status
	}
	if status != 0 && status != 1 {
		return SaveQuickReplyRequest{}, apperrors.New(consts.StatusBadRequest, "quick reply status is invalid")
	}
	req.Status = &status
	return req, nil
}
