package content

import "context"

// PublicAgreement resolves the active mode and returns the matching template.
// A missing or disabled row is reported as configured=false so public pages can
// keep their built-in legal copy.
func (s *Service) PublicAgreement(ctx context.Context, docKey, lang string) (PublicAgreementResult, error) {
	key, err := normalizeAgreementDocKey(docKey)
	if err != nil {
		return PublicAgreementResult{}, err
	}
	mode, err := s.repo.AgreementActiveMode(ctx)
	if err != nil {
		return PublicAgreementResult{}, err
	}
	if _, ok := agreementModes[mode]; !ok {
		mode = AgreementModeWelfare
	}
	resolvedLang := normalizeAgreementLang(lang)
	result := PublicAgreementResult{DocKey: key, Mode: mode, Lang: resolvedLang}
	item, ok, err := s.repo.AgreementTemplate(ctx, key, mode, resolvedLang)
	if err != nil {
		return PublicAgreementResult{}, err
	}
	if !ok || item.Status != 1 {
		return result, nil
	}
	result.Configured = true
	result.ContentHTML = SanitizeNoticeHTML(item.ContentHTML)
	result.EffectiveDate = item.EffectiveDate
	result.Summary = item.Summary
	result.Title = item.Title
	result.UpdateTime = item.UpdateTime
	result.Version = item.Version
	return result, nil
}

func (s *Service) AdminAgreements(ctx context.Context, principal Principal) (AdminAgreementsResult, error) {
	if err := s.authz.Require(ctx, principal, "system:agreement:view"); err != nil {
		return AdminAgreementsResult{}, err
	}
	return s.agreementOverview(ctx)
}

func (s *Service) SaveAgreementMode(ctx context.Context, principal Principal, req SaveAgreementModeRequest) error {
	if err := s.authz.Require(ctx, principal, "system:agreement:update"); err != nil {
		return err
	}
	mode, err := normalizeAgreementMode(req.Mode)
	if err != nil {
		return err
	}
	return s.repo.SaveAgreementActiveMode(ctx, mode)
}

func (s *Service) SaveAgreementTemplate(ctx context.Context, principal Principal, docKey, mode, lang string, req SaveAgreementTemplateRequest) error {
	if err := s.authz.Require(ctx, principal, "system:agreement:update"); err != nil {
		return err
	}
	key, err := normalizeAgreementDocKey(docKey)
	if err != nil {
		return err
	}
	normalizedMode, err := normalizeAgreementMode(mode)
	if err != nil {
		return err
	}
	item, err := normalizeAgreementTemplate(key, normalizedMode, normalizeAgreementLang(lang), req)
	if err != nil {
		return err
	}
	return s.repo.UpsertAgreementTemplate(ctx, item, principal.ID)
}

func (s *Service) agreementOverview(ctx context.Context) (AdminAgreementsResult, error) {
	mode, err := s.repo.AgreementActiveMode(ctx)
	if err != nil {
		return AdminAgreementsResult{}, err
	}
	if _, ok := agreementModes[mode]; !ok {
		mode = AgreementModeWelfare
	}
	items, err := s.repo.AgreementTemplates(ctx)
	if err != nil {
		return AdminAgreementsResult{}, err
	}
	return AdminAgreementsResult{ActiveMode: mode, Items: items}, nil
}
