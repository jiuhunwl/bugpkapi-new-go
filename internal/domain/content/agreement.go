package content

import (
	"strings"
	"time"
	"unicode/utf8"

	apperrors "bugpk-api/internal/pkg/errors"

	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

const (
	AgreementModeWelfare    = "welfare"
	AgreementModeCommercial = "commercial"

	AgreementDocPayment = "payment"
	AgreementDocPrivacy = "privacy"
	AgreementDocTerms   = "terms"

	AgreementLangZhCN = "zh-CN"
	AgreementLangEnUS = "en-US"

	maxAgreementHTMLBytes    = 1 << 20
	maxAgreementTitleRunes   = 200
	maxAgreementSummaryRunes = 500
	maxAgreementVersionRunes = 32
	agreementDateLayout      = "2006-01-02"
)

var agreementDocKeys = map[string]struct{}{
	AgreementDocPayment: {},
	AgreementDocPrivacy: {},
	AgreementDocTerms:   {},
}

var agreementModes = map[string]struct{}{
	AgreementModeWelfare:    {},
	AgreementModeCommercial: {},
}

// AgreementTemplate is one editable document variant. The public reader only
// ever sees the row selected by active mode + request language.
type AgreementTemplate struct {
	ContentHTML   string `json:"contentHtml"`
	DocKey        string `json:"docKey"`
	EffectiveDate string `json:"effectiveDate,omitempty"`
	Lang          string `json:"lang"`
	Mode          string `json:"mode"`
	Status        int    `json:"status"`
	Summary       string `json:"summary"`
	Title         string `json:"title"`
	UpdateTime    string `json:"updateTime,omitempty"`
	Version       string `json:"version"`
}

type AdminAgreementsResult struct {
	ActiveMode string              `json:"activeMode"`
	Items      []AgreementTemplate `json:"items"`
}

// PublicAgreementResult keeps Configured=false explicit so the frontend can
// fall back to its built-in legal copy instead of rendering an empty page.
type PublicAgreementResult struct {
	Configured    bool   `json:"configured"`
	ContentHTML   string `json:"contentHtml,omitempty"`
	DocKey        string `json:"docKey"`
	EffectiveDate string `json:"effectiveDate,omitempty"`
	Lang          string `json:"lang"`
	Mode          string `json:"mode"`
	Summary       string `json:"summary,omitempty"`
	Title         string `json:"title,omitempty"`
	UpdateTime    string `json:"updateTime,omitempty"`
	Version       string `json:"version,omitempty"`
}

type SaveAgreementModeRequest struct {
	Mode string `json:"mode"`
}

type SaveAgreementTemplateRequest struct {
	ContentHTML   string `json:"contentHtml"`
	EffectiveDate string `json:"effectiveDate"`
	Status        *int   `json:"status"`
	Summary       string `json:"summary"`
	Title         string `json:"title"`
	Version       string `json:"version"`
}

func normalizeAgreementDocKey(value string) (string, error) {
	key := strings.ToLower(strings.TrimSpace(value))
	if _, ok := agreementDocKeys[key]; !ok {
		return "", apperrors.New(consts.StatusBadRequest, "agreement document key is invalid")
	}
	return key, nil
}

func normalizeAgreementMode(value string) (string, error) {
	mode := strings.ToLower(strings.TrimSpace(value))
	if _, ok := agreementModes[mode]; !ok {
		return "", apperrors.New(consts.StatusBadRequest, "agreement mode is invalid")
	}
	return mode, nil
}

// normalizeAgreementLang accepts the common language tag spellings a proxy or
// SDK may send; every unsupported value falls back to the default site
// language instead of surfacing an error to a public reader.
func normalizeAgreementLang(value string) string {
	normalized := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(value), "_", "-"))
	if index := strings.IndexByte(normalized, ','); index >= 0 {
		normalized = strings.TrimSpace(normalized[:index])
	}
	switch {
	case normalized == "en", normalized == "en-us", strings.HasPrefix(normalized, "en-"):
		return AgreementLangEnUS
	default:
		return AgreementLangZhCN
	}
}

func normalizeAgreementTemplate(docKey, mode, lang string, req SaveAgreementTemplateRequest) (AgreementTemplate, error) {
	title := strings.TrimSpace(req.Title)
	if title == "" {
		return AgreementTemplate{}, apperrors.New(consts.StatusBadRequest, "agreement title is required")
	}
	if utf8.RuneCountInString(title) > maxAgreementTitleRunes {
		return AgreementTemplate{}, apperrors.New(consts.StatusBadRequest, "agreement title is too long")
	}

	summary := strings.TrimSpace(req.Summary)
	if utf8.RuneCountInString(summary) > maxAgreementSummaryRunes {
		return AgreementTemplate{}, apperrors.New(consts.StatusBadRequest, "agreement summary is too long")
	}

	version := strings.TrimSpace(req.Version)
	if version == "" {
		version = "v1.0"
	}
	if utf8.RuneCountInString(version) > maxAgreementVersionRunes {
		return AgreementTemplate{}, apperrors.New(consts.StatusBadRequest, "agreement version is too long")
	}

	effectiveDate := strings.TrimSpace(req.EffectiveDate)
	if effectiveDate != "" {
		parsed, err := time.Parse(agreementDateLayout, effectiveDate)
		if err != nil {
			return AgreementTemplate{}, apperrors.New(consts.StatusBadRequest, "agreement effective date must be YYYY-MM-DD")
		}
		effectiveDate = parsed.Format(agreementDateLayout)
	}

	rawHTML := strings.TrimSpace(req.ContentHTML)
	if len(rawHTML) > maxAgreementHTMLBytes {
		return AgreementTemplate{}, apperrors.New(consts.StatusBadRequest, "agreement content is too large")
	}
	sanitized := strings.TrimSpace(SanitizeNoticeHTML(rawHTML))
	if sanitized == "" {
		return AgreementTemplate{}, apperrors.New(consts.StatusBadRequest, "agreement content is required")
	}

	status := 1
	if req.Status != nil {
		if *req.Status != 0 && *req.Status != 1 {
			return AgreementTemplate{}, apperrors.New(consts.StatusBadRequest, "agreement status must be 0 or 1")
		}
		status = *req.Status
	}

	return AgreementTemplate{
		ContentHTML:   sanitized,
		DocKey:        docKey,
		EffectiveDate: effectiveDate,
		Lang:          lang,
		Mode:          mode,
		Status:        status,
		Summary:       summary,
		Title:         title,
		Version:       version,
	}, nil
}
