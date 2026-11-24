package notification

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"bugpk-api/internal/config"
	"bugpk-api/internal/pkg/emaillang"
	"bugpk-api/internal/pkg/masking"
	"bugpk-api/internal/pkg/smtpmail"

	"go.uber.org/zap"
)

var errEmailDeliveryDisabled = errors.New("email notification delivery is disabled")

type EmailConfigProvider interface {
	EmailMessageConfig(context.Context) (config.EmailMessageConfig, bool, error)
	SiteName(context.Context) (string, error)
}

type mailSender func(
	context.Context,
	config.EmailMessageConfig,
	string,
	string,
	string,
) error

type outboxRepository interface {
	Claim(context.Context, int) ([]Entry, error)
	Enqueue(context.Context, EnqueueRequest) error
	MarkRetry(context.Context, Entry, error, time.Time, bool) error
	MarkSent(context.Context, int64) error
}

type Service struct {
	fallback config.EmailMessageConfig
	logger   *zap.Logger
	provider EmailConfigProvider
	repo     outboxRepository
	sendMail mailSender
}

func NewService(
	repo *Repository,
	fallback config.EmailMessageConfig,
	provider EmailConfigProvider,
	logger *zap.Logger,
) *Service {
	if logger == nil {
		logger = zap.NewNop()
	}
	service := &Service{
		fallback: fallback,
		logger:   logger,
		provider: provider,
		repo:     repo,
	}
	service.sendMail = service.sendSMTP
	return service
}

func (s *Service) Enqueue(ctx context.Context, req EnqueueRequest) error {
	if s == nil || s.repo == nil {
		return errors.New("notification service is unavailable")
	}
	req.TemplateKey = strings.TrimSpace(req.TemplateKey)
	if !supportedTemplate(req.TemplateKey) {
		return fmt.Errorf("unsupported notification template %q", req.TemplateKey)
	}
	parsed, err := mail.ParseAddress(strings.TrimSpace(req.Recipient))
	if err != nil || parsed.Address == "" {
		return errors.New("notification recipient is invalid")
	}
	req.Recipient = strings.ToLower(parsed.Address)
	// Freeze the final supported language in the outbox. Workers must never
	// re-evaluate a later browser/site setting, otherwise a delayed retry could
	// switch templates after the business action completed.
	req.Language = emaillang.Resolve(req.Language, "", "", "")
	if req.Payload == nil {
		req.Payload = map[string]any{}
	}
	payload, err := json.Marshal(req.Payload)
	if err != nil {
		return fmt.Errorf("marshal notification payload: %w", err)
	}
	if len(payload) > 32*1024 {
		return errors.New("notification payload is too large")
	}
	req.DedupeKey = strings.TrimSpace(req.DedupeKey)
	if req.DedupeKey == "" {
		sum := sha256.Sum256([]byte(strings.Join([]string{
			req.TemplateKey,
			req.Recipient,
			string(payload),
		}, "|")))
		req.DedupeKey = "auto:" + hex.EncodeToString(sum[:])
	}
	if len(req.DedupeKey) > 191 {
		sum := sha256.Sum256([]byte(req.DedupeKey))
		req.DedupeKey = "hash:" + hex.EncodeToString(sum[:])
	}
	if err := s.repo.Enqueue(ctx, req); err != nil {
		s.logger.Error("enqueue notification failed",
			zap.String("template", req.TemplateKey),
			// Recipient addresses are personal data.  Keep the diagnostic field
			// useful for correlating a delivery failure without writing the full
			// mailbox to application logs.
			zap.String("recipient", masking.Email(req.Recipient)),
			zap.String("error", masking.Error(err)),
		)
		return err
	}
	return nil
}

func (s *Service) deliver(ctx context.Context, entry Entry) error {
	var payload map[string]any
	if err := json.Unmarshal([]byte(entry.Payload), &payload); err != nil {
		return permanentError{err: fmt.Errorf("decode notification payload: %w", err)}
	}
	cfg, siteName, err := s.emailConfig(ctx)
	if err != nil {
		return err
	}
	rendered, err := Render(entry.TemplateKey, entry.Language, siteName, payload)
	if err != nil {
		return err
	}
	timeout := cfg.SMTPTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	deliveryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout+time.Second)
	defer cancel()
	return s.sendMail(
		deliveryCtx,
		cfg,
		entry.Recipient,
		rendered.Subject,
		rendered.Body,
	)
}

func (s *Service) emailConfig(ctx context.Context) (config.EmailMessageConfig, string, error) {
	cfg := s.fallback
	siteName := "BugPk-Api"
	if s.provider != nil {
		dbCfg, ok, err := s.provider.EmailMessageConfig(ctx)
		if err != nil {
			return config.EmailMessageConfig{}, "", err
		}
		if ok {
			cfg = mergeEmailConfig(cfg, dbCfg)
		}
		loadedSiteName, err := s.provider.SiteName(ctx)
		if err != nil {
			return config.EmailMessageConfig{}, "", err
		}
		if strings.TrimSpace(loadedSiteName) != "" {
			siteName = strings.TrimSpace(loadedSiteName)
		}
	}
	if strings.TrimSpace(cfg.Provider) == "" {
		cfg.Provider = "smtp"
	}
	if !cfg.Enabled || !strings.EqualFold(strings.TrimSpace(cfg.Provider), "smtp") {
		return config.EmailMessageConfig{}, siteName, errEmailDeliveryDisabled
	}
	if strings.TrimSpace(cfg.SMTPFromName) == "" || cfg.SMTPFromName == "BugPk-Api" {
		cfg.SMTPFromName = siteName
	}
	if cfg.SMTPTimeout <= 0 {
		cfg.SMTPTimeout = 10 * time.Second
	}
	return cfg, siteName, nil
}

func mergeEmailConfig(base, override config.EmailMessageConfig) config.EmailMessageConfig {
	if strings.TrimSpace(override.Provider) == "" {
		override.Provider = base.Provider
	}
	if strings.TrimSpace(override.SMTPHost) == "" {
		override.SMTPHost = base.SMTPHost
	}
	if override.SMTPPort <= 0 {
		override.SMTPPort = base.SMTPPort
	}
	if strings.TrimSpace(override.SMTPUsername) == "" {
		override.SMTPUsername = base.SMTPUsername
	}
	if strings.TrimSpace(override.SMTPPassword) == "" {
		override.SMTPPassword = base.SMTPPassword
	}
	if strings.TrimSpace(override.SMTPFrom) == "" {
		override.SMTPFrom = base.SMTPFrom
	}
	if strings.TrimSpace(override.SMTPFromName) == "" {
		override.SMTPFromName = base.SMTPFromName
	}
	if override.SMTPTimeout <= 0 {
		override.SMTPTimeout = base.SMTPTimeout
	}
	return override
}

func (s *Service) sendSMTP(
	ctx context.Context,
	cfg config.EmailMessageConfig,
	to string,
	subject string,
	body string,
) error {
	return smtpmail.Send(ctx, smtpmail.Config{
		FromEmail: cfg.SMTPFrom,
		FromName:  cfg.SMTPFromName,
		Host:      cfg.SMTPHost,
		Password:  cfg.SMTPPassword,
		Port:      cfg.SMTPPort,
		TLS:       cfg.SMTPTLS,
		Timeout:   cfg.SMTPTimeout,
		Username:  cfg.SMTPUsername,
	}, to, subject, body)
}

func supportedTemplate(value string) bool {
	switch value {
	case TemplateTicketCreated,
		TemplateTicketUserReply,
		TemplateTicketUserClosed,
		TemplateTicketStaffReply,
		TemplateTicketStatus,
		TemplateFriendReceived,
		TemplateFriendPending,
		TemplateFriendApproved,
		TemplateFriendRejected,
		TemplateFriendDeleted,
		TemplatePaymentPaid,
		TemplateBroadcast:
		return true
	default:
		return false
	}
}
