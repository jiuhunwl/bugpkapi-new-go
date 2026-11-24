package content

import (
	"strings"
	"testing"
)

func TestNormalizeAgreementLang(t *testing.T) {
	cases := map[string]string{
		"":                AgreementLangZhCN,
		"zh":              AgreementLangZhCN,
		"zh-CN":           AgreementLangZhCN,
		"zh_cn":           AgreementLangZhCN,
		"en":              AgreementLangEnUS,
		"en-US":           AgreementLangEnUS,
		"en-GB":           AgreementLangEnUS,
		" en-us,en;q=0.9": AgreementLangEnUS,
		"fr-FR":           AgreementLangZhCN,
	}
	for input, expected := range cases {
		if got := normalizeAgreementLang(input); got != expected {
			t.Fatalf("normalizeAgreementLang(%q) = %q, want %q", input, got, expected)
		}
	}
}

func TestNormalizeAgreementDocKeyAndMode(t *testing.T) {
	if key, err := normalizeAgreementDocKey(" PAYMENT "); err != nil || key != AgreementDocPayment {
		t.Fatalf("doc key normalization failed: %q %v", key, err)
	}
	if _, err := normalizeAgreementDocKey("refund"); err == nil {
		t.Fatal("expected unknown document key to be rejected")
	}
	if mode, err := normalizeAgreementMode("Commercial"); err != nil || mode != AgreementModeCommercial {
		t.Fatalf("mode normalization failed: %q %v", mode, err)
	}
	if _, err := normalizeAgreementMode("free"); err == nil {
		t.Fatal("expected unknown mode to be rejected")
	}
}

func TestNormalizeAgreementTemplateSanitizesAndDefaults(t *testing.T) {
	status := 1
	item, err := normalizeAgreementTemplate(AgreementDocPayment, AgreementModeWelfare, AgreementLangZhCN, SaveAgreementTemplateRequest{
		Title:         " 公益支付协议 ",
		Summary:       " 摘要 ",
		Version:       "",
		EffectiveDate: "2026-11-16",
		ContentHTML:   `<h2>一、条款</h2><p>正文</p><script>alert(1)</script>`,
		Status:        &status,
	})
	if err != nil {
		t.Fatal(err)
	}
	if item.Title != "公益支付协议" || item.Summary != "摘要" || item.Version != "v1.0" {
		t.Fatalf("unexpected normalized template: %+v", item)
	}
	if strings.Contains(strings.ToLower(item.ContentHTML), "script") {
		t.Fatalf("sanitizer retained script: %q", item.ContentHTML)
	}
	if !strings.Contains(item.ContentHTML, "<h2>一、条款</h2>") {
		t.Fatalf("sanitizer lost safe content: %q", item.ContentHTML)
	}
}

func TestNormalizeAgreementTemplateRejectsInvalidInput(t *testing.T) {
	base := SaveAgreementTemplateRequest{
		Title:       "标题",
		ContentHTML: "<p>正文</p>",
	}
	if _, err := normalizeAgreementTemplate(AgreementDocTerms, AgreementModeWelfare, AgreementLangZhCN, SaveAgreementTemplateRequest{ContentHTML: "<p>x</p>"}); err == nil {
		t.Fatal("expected empty title to be rejected")
	}
	invalidDate := base
	invalidDate.EffectiveDate = "2026/11/16"
	if _, err := normalizeAgreementTemplate(AgreementDocTerms, AgreementModeWelfare, AgreementLangZhCN, invalidDate); err == nil {
		t.Fatal("expected invalid effective date to be rejected")
	}
	emptyContent := base
	emptyContent.ContentHTML = `<script>alert(1)</script>`
	if _, err := normalizeAgreementTemplate(AgreementDocTerms, AgreementModeWelfare, AgreementLangZhCN, emptyContent); err == nil {
		t.Fatal("expected script-only content to be rejected")
	}
	badStatus := base
	status := 2
	badStatus.Status = &status
	if _, err := normalizeAgreementTemplate(AgreementDocTerms, AgreementModeWelfare, AgreementLangZhCN, badStatus); err == nil {
		t.Fatal("expected invalid status to be rejected")
	}
}
