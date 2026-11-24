package systemsettings

import (
	"context"
	"regexp"
	"strings"

	"bugpk-api/internal/domain/rbac"
	apperrors "bugpk-api/internal/pkg/errors"

	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

const maxCustomCodeBytes = 256 << 10

var (
	completeStyleTags  = regexp.MustCompile(`(?is)^\s*(?:<style(?:\s[^>]*)?>.*?</style>\s*)+$`)
	completeScriptTags = regexp.MustCompile(`(?is)^\s*(?:<script(?:\s[^>]*)?>.*?</script>\s*)+$`)
)

type CustomCodeConfig struct {
	CSSCode    string `json:"cssCode"`
	Enabled    int    `json:"enabled"`
	JSCode     string `json:"jsCode"`
	UpdateTime string `json:"updateTime,omitempty"`
}

func (s *Service) PublicCustomCode(ctx context.Context) (CustomCodeConfig, error) {
	config, err := s.repo.CustomCodeConfig(ctx)
	if err != nil {
		return CustomCodeConfig{}, err
	}
	if config.Enabled != 1 {
		return CustomCodeConfig{Enabled: 0, UpdateTime: config.UpdateTime}, nil
	}
	return config, nil
}

func (s *Service) AdminCustomCode(ctx context.Context, principal rbac.Principal) (CustomCodeConfig, error) {
	if err := s.authz.Require(ctx, principal, "system:custom-code:view"); err != nil {
		return CustomCodeConfig{}, err
	}
	return s.repo.CustomCodeConfig(ctx)
}

func (s *Service) SaveCustomCode(ctx context.Context, principal rbac.Principal, req CustomCodeConfig) error {
	if err := s.authz.Require(ctx, principal, "system:custom-code:update"); err != nil {
		return err
	}
	config, err := normalizeCustomCode(req)
	if err != nil {
		return err
	}
	return s.repo.SaveCustomCodeConfig(ctx, config, principal.ID)
}

func normalizeCustomCode(req CustomCodeConfig) (CustomCodeConfig, error) {
	if req.Enabled != 0 && req.Enabled != 1 {
		return CustomCodeConfig{}, apperrors.New(consts.StatusBadRequest, "自定义代码状态不合法")
	}
	css := strings.TrimPrefix(req.CSSCode, "\ufeff")
	js := strings.TrimPrefix(req.JSCode, "\ufeff")
	if len(css) > maxCustomCodeBytes || len(js) > maxCustomCodeBytes {
		return CustomCodeConfig{}, apperrors.New(consts.StatusRequestEntityTooLarge, "CSS 或 JavaScript 不能超过256 KiB")
	}
	if strings.ContainsRune(css, '\x00') || strings.ContainsRune(js, '\x00') {
		return CustomCodeConfig{}, apperrors.New(consts.StatusBadRequest, "自定义代码不能包含 NUL 字符")
	}
	if strings.TrimSpace(css) != "" && !completeStyleTags.MatchString(css) {
		return CustomCodeConfig{}, apperrors.New(consts.StatusBadRequest, "CSS code must contain complete <style> tags")
	}
	if strings.TrimSpace(js) != "" && !completeScriptTags.MatchString(js) {
		return CustomCodeConfig{}, apperrors.New(consts.StatusBadRequest, "JavaScript code must contain complete <script> tags")
	}
	return CustomCodeConfig{CSSCode: css, Enabled: req.Enabled, JSCode: js}, nil
}
