package content

import (
	"context"
	"encoding/json"
	"regexp"
	"sort"
	"strings"
	"time"

	apperrors "bugpk-api/internal/pkg/errors"

	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"github.com/redis/go-redis/v9"
)

const (
	maxDonationMethods = 8
	donationQRPrefix   = "/upload/donation-qr/"
)

var (
	donationMethodKeyPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)
	donationColorPattern     = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)
	donationIconPattern      = regexp.MustCompile(`^(?:lucide|mdi|simple-icons):[a-z0-9][a-z0-9-]{0,95}$`)
)

func (s *Service) PublicDonationConfig(ctx context.Context) (PublicDonationConfig, error) {
	cache, ttl := s.donationCacheSnapshot()
	if cache != nil {
		if raw, err := cache.Get(ctx, donationPublicCacheKey).Bytes(); err == nil {
			var cached PublicDonationConfig
			if json.Unmarshal(raw, &cached) == nil {
				s.localizePublicDonationConfig(ctx, &cached)
				return cached, nil
			}
		}
	}
	cfg, err := s.repo.DonationConfig(ctx)
	if err != nil {
		return PublicDonationConfig{}, err
	}
	public := publicDonationConfig(cfg)
	if cache != nil {
		if raw, marshalErr := json.Marshal(public); marshalErr == nil {
			if ttl <= 0 {
				ttl = 5 * time.Second
			}
			_ = cache.Set(ctx, donationPublicCacheKey, raw, ttl).Err()
		}
	}
	s.localizePublicDonationConfig(ctx, &public)
	return public, nil
}

// localizePublicDonationConfig applies the donation dictionary after the
// locale-neutral public payload leaves the cache. The cache must keep the
// default-language object so one request's language can never leak into the
// next request through Redis.
func (s *Service) localizePublicDonationConfig(ctx context.Context, cfg *PublicDonationConfig) {
	if s.localizer == nil || cfg == nil {
		return
	}
	translations := s.localizer.Localize(ctx, "donation", "1")
	if len(translations) == 0 {
		return
	}
	apply := func(target *string, field string) {
		if value, ok := translations[field]; ok {
			*target = value
		}
	}
	apply(&cfg.Eyebrow, "eyebrow")
	apply(&cfg.Title, "title")
	apply(&cfg.Description, "description")
	apply(&cfg.ScanTitle, "scanTitle")
	apply(&cfg.ScanDescription, "scanDescription")
	apply(&cfg.Thanks, "thanks")
}

func (s *Service) AdminDonationConfig(ctx context.Context, principal Principal) (DonationConfig, error) {
	if err := s.authz.Require(ctx, principal, "donation:config:view"); err != nil {
		return DonationConfig{}, err
	}
	return s.repo.DonationConfig(ctx)
}

func (s *Service) SaveDonationConfig(ctx context.Context, principal Principal, req SaveDonationConfigRequest) error {
	if err := s.authz.Require(ctx, principal, "donation:config:update"); err != nil {
		return err
	}
	cfg, err := normalizeDonationConfig(req, s.systemPrefix)
	if err != nil {
		return err
	}
	if err := s.repo.SaveDonationConfig(ctx, cfg, principal.ID); err != nil {
		return err
	}
	s.invalidateDonationConfig(ctx)
	return nil
}

func (s *Service) ValidateDonationQRCodeUpload(ctx context.Context, principal Principal) error {
	return s.authz.Require(ctx, principal, "donation:upload-qr")
}

func normalizeDonationConfig(req SaveDonationConfigRequest, systemPrefix string) (DonationConfig, error) {
	eyebrow, err := boundedText("eyebrow", req.Eyebrow, 64, true)
	if err != nil {
		return DonationConfig{}, err
	}
	title, err := boundedText("title", req.Title, 120, true)
	if err != nil {
		return DonationConfig{}, err
	}
	description, err := boundedText("description", req.Description, 500, false)
	if err != nil {
		return DonationConfig{}, err
	}
	scanTitle, err := boundedText("scanTitle", req.ScanTitle, 120, true)
	if err != nil {
		return DonationConfig{}, err
	}
	scanDescription, err := boundedText("scanDescription", req.ScanDescription, 255, false)
	if err != nil {
		return DonationConfig{}, err
	}
	thanks, err := boundedText("thanks", req.Thanks, 255, false)
	if err != nil {
		return DonationConfig{}, err
	}
	if req.Enabled != 0 && req.Enabled != 1 {
		return DonationConfig{}, apperrors.New(consts.StatusBadRequest, "enabled must be 0 or 1")
	}
	if len(req.Methods) > maxDonationMethods {
		return DonationConfig{}, apperrors.New(consts.StatusBadRequest, "too many donation methods")
	}
	methods := make([]DonationMethod, 0, len(req.Methods))
	seenKeys := make(map[string]struct{}, len(req.Methods))
	enabledMethods := 0
	for _, method := range req.Methods {
		normalized, methodErr := normalizeDonationMethod(method, systemPrefix)
		if methodErr != nil {
			return DonationConfig{}, methodErr
		}
		if _, exists := seenKeys[normalized.Key]; exists {
			return DonationConfig{}, apperrors.New(consts.StatusBadRequest, "donation method key must be unique")
		}
		seenKeys[normalized.Key] = struct{}{}
		if normalized.Enabled == 1 {
			enabledMethods++
		}
		methods = append(methods, normalized)
	}
	if req.Enabled == 1 && enabledMethods == 0 {
		return DonationConfig{}, apperrors.New(consts.StatusBadRequest, "at least one donation method is required when enabled")
	}
	sort.SliceStable(methods, func(i, j int) bool { return methods[i].SortNo < methods[j].SortNo })
	return DonationConfig{
		Description:     description,
		Enabled:         req.Enabled,
		Eyebrow:         eyebrow,
		ID:              1,
		Methods:         methods,
		ScanDescription: scanDescription,
		ScanTitle:       scanTitle,
		Thanks:          thanks,
		Title:           title,
	}, nil
}

func normalizeDonationMethod(method DonationMethod, systemPrefix string) (DonationMethod, error) {
	key := strings.ToLower(strings.TrimSpace(method.Key))
	if !donationMethodKeyPattern.MatchString(key) {
		return DonationMethod{}, apperrors.New(consts.StatusBadRequest, "donation method key is invalid")
	}
	name, err := boundedText("method.name", method.Name, 32, true)
	if err != nil {
		return DonationMethod{}, err
	}
	icon := strings.TrimSpace(method.Icon)
	if icon == "" {
		icon = "lucide:heart"
	}
	if !donationIconPattern.MatchString(icon) {
		return DonationMethod{}, apperrors.New(consts.StatusBadRequest, "donation method icon is invalid")
	}
	color := strings.TrimSpace(method.Color)
	if color == "" {
		color = "#1677ff"
	}
	if !donationColorPattern.MatchString(color) {
		return DonationMethod{}, apperrors.New(consts.StatusBadRequest, "donation method color is invalid")
	}
	if method.Enabled != 0 && method.Enabled != 1 {
		return DonationMethod{}, apperrors.New(consts.StatusBadRequest, "donation method enabled must be 0 or 1")
	}
	if method.SortNo < -100000 || method.SortNo > 100000 {
		return DonationMethod{}, apperrors.New(consts.StatusBadRequest, "donation method sortNo is out of range")
	}
	qrCodeURL := strings.TrimSpace(method.QRCodeURL)
	if qrCodeURL != "" && !validImageURL(qrCodeURL, strings.TrimRight(systemPrefix, "/")+donationQRPrefix) {
		return DonationMethod{}, apperrors.New(consts.StatusBadRequest, "donation method qrCodeUrl is invalid")
	}
	if method.Enabled == 1 && qrCodeURL == "" {
		return DonationMethod{}, apperrors.New(consts.StatusBadRequest, "enabled donation method requires qrCodeUrl")
	}
	tip, err := boundedText("method.tip", method.Tip, 120, false)
	if err != nil {
		return DonationMethod{}, err
	}
	return DonationMethod{
		Color: color, Enabled: method.Enabled, Icon: icon, Key: key,
		Name: name, QRCodeURL: qrCodeURL, SortNo: method.SortNo, Tip: tip,
	}, nil
}

func publicDonationConfig(cfg DonationConfig) PublicDonationConfig {
	methods := make([]DonationMethod, 0, len(cfg.Methods))
	if cfg.Enabled == 1 {
		for _, method := range cfg.Methods {
			if method.Enabled == 1 {
				methods = append(methods, method)
			}
		}
	}
	return PublicDonationConfig{
		Description: cfg.Description, Enabled: cfg.Enabled, Eyebrow: cfg.Eyebrow,
		Methods: methods, ScanDescription: cfg.ScanDescription,
		ScanTitle: cfg.ScanTitle, Thanks: cfg.Thanks, Title: cfg.Title,
	}
}

const donationPublicCacheKey = "content:public:donation-config:v1"

func (s *Service) invalidateDonationConfig(ctx context.Context) {
	cache, _ := s.donationCacheSnapshot()
	if cache != nil {
		_ = cache.Del(ctx, donationPublicCacheKey).Err()
	}
}

func (s *Service) donationCacheSnapshot() (*redis.Client, time.Duration) {
	s.publicCacheMu.Lock()
	defer s.publicCacheMu.Unlock()
	return s.publicCache, s.publicCacheTTL
}
