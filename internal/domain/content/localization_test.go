package content

import (
	"context"
	"testing"
)

type staticLocalizer struct {
	values map[string]map[string]map[string]string
}

func (l staticLocalizer) Locale(context.Context) string {
	return "en-US"
}

func (l staticLocalizer) Localize(_ context.Context, resourceType, key string) map[string]string {
	return l.values[resourceType][key]
}

func (l staticLocalizer) LocalizeBatch(_ context.Context, resourceType string, keys []string) map[string]map[string]string {
	result := make(map[string]map[string]string)
	for _, key := range keys {
		if values := l.values[resourceType][key]; len(values) > 0 {
			result[key] = values
		}
	}
	return result
}

func TestLocalizeSiteConfig(t *testing.T) {
	service := &Service{localizer: staticLocalizer{values: map[string]map[string]map[string]string{
		"site": {"1": {"siteName": "BugPk", "slogan": "Open API"}},
	}}}
	cfg := SiteConfig{SiteName: "默认站点", Slogan: "默认标语"}
	service.localizeSiteConfig(context.Background(), &cfg)
	if cfg.SiteName != "BugPk" || cfg.Slogan != "Open API" {
		t.Fatalf("site config not localized: %+v", cfg)
	}
}

func TestLocalizeNoticeItems(t *testing.T) {
	service := &Service{localizer: staticLocalizer{values: map[string]map[string]map[string]string{
		"notice": {
			"1": {"title": "Maintenance", "summary": "Tonight", "contentHtml": "<p>Tonight</p>"},
			"2": {"title": "Launch"},
		},
	}}}
	items := []NoticeItem{
		{ID: "1", Title: "维护", ContentHTML: "<p>今晚</p>"},
		{ID: "2", Title: "上线", Summary: "默认摘要"},
	}
	service.localizeNoticeItems(context.Background(), items)
	if items[0].Title != "Maintenance" || items[0].Summary != "Tonight" || items[0].ContentHTML != "<p>Tonight</p>" {
		t.Fatalf("notice 1 not localized: %+v", items[0])
	}
	if items[1].Title != "Launch" || items[1].Summary != "默认摘要" {
		t.Fatalf("notice 2 fallback broken: %+v", items[1])
	}
}

func TestLocalizePublicDonationConfig(t *testing.T) {
	service := &Service{localizer: staticLocalizer{values: map[string]map[string]map[string]string{
		"donation": {"1": {
			"eyebrow": "Support", "title": "Support BugPk-Api",
			"description": "If it helps, support the project.",
			"scanTitle":   "Scan to support", "scanDescription": "Choose a payment method.",
			"thanks": "Thank you for your support.",
		}},
	}}}
	cfg := PublicDonationConfig{
		Eyebrow: "赞赏", Title: "赞赏支持", Description: "如果对你有帮助，欢迎支持。",
		ScanTitle: "扫码赞赏", ScanDescription: "选择支付方式。", Thanks: "感谢支持。",
	}
	service.localizePublicDonationConfig(context.Background(), &cfg)
	if cfg.Eyebrow != "Support" || cfg.Title != "Support BugPk-Api" || cfg.Description != "If it helps, support the project." {
		t.Fatalf("donation header not localized: %+v", cfg)
	}
	if cfg.ScanTitle != "Scan to support" || cfg.ScanDescription != "Choose a payment method." || cfg.Thanks != "Thank you for your support." {
		t.Fatalf("donation scan fields not localized: %+v", cfg)
	}
}

func TestLocalizeHomeOverviewDoesNotMutateCachedSlice(t *testing.T) {
	service := &Service{localizer: staticLocalizer{values: map[string]map[string]map[string]string{
		"interface": {"42": {"name": "Weather API", "remark": "Global weather"}},
		"notice":    {"7": {"title": "Welcome"}},
		"site":      {"1": {"siteName": "BugPk"}},
	}}}
	original := HomeOverview{
		SiteConfig: SiteConfig{SiteName: "默认站点"},
		HomeNotice: HomeTicker{Enabled: 1, NoticeID: "7", Content: "默认公告"},
		HotAPIs:    []HotAPIItem{{ID: "42", Name: "天气", Description: "全球天气"}},
	}
	localized := service.localizeHomeOverview(context.Background(), original)
	if localized.SiteConfig.SiteName != "BugPk" || localized.HomeNotice.Content != "Welcome" {
		t.Fatalf("overview text not localized: %+v", localized)
	}
	if localized.HotAPIs[0].Name != "Weather API" || localized.HotAPIs[0].Description != "Global weather" {
		t.Fatalf("hot api not localized: %+v", localized.HotAPIs[0])
	}
	if original.HotAPIs[0].Name != "天气" {
		t.Fatalf("cached hot api slice was mutated: %+v", original.HotAPIs[0])
	}
}
