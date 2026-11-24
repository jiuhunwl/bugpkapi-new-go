package content

import (
	"strings"
	"testing"
)

func TestSanitizeHTMLStripsDangerousContent(t *testing.T) {
	input := `<p onclick="alert(1)">Hello <a href="javascript:alert(1)" target="_blank">bad</a><img src="/system-api/upload/notice-image/a.png" onerror="x"></p><script>alert(1)</script>`
	got := sanitizeHTML(input)
	lower := strings.ToLower(got)
	for _, bad := range []string{"script", "onclick", "onerror", "javascript:"} {
		if strings.Contains(lower, bad) {
			t.Fatalf("expected %q to be stripped from %q", bad, got)
		}
	}
	if !strings.Contains(got, `<img src="/system-api/upload/notice-image/a.png"`) {
		t.Fatalf("expected safe local image src to remain, got %q", got)
	}
}

func TestSanitizeHTMLHandlesMalformedParserDifferentials(t *testing.T) {
	input := `<div><svg><style><img src=x onerror=alert(1)></style></svg><a href="java&#x73;cript:alert(1)" target="_blank">entity</a><a href="//evil.example/path">protocol</a><template><img src="https://evil.example/a.png"></template><p>safe</p></div>`
	got := sanitizeHTML(input)
	lower := strings.ToLower(got)
	for _, bad := range []string{"javascript", "onerror", "<svg", "<style", "//evil.example", "<template", "evil.example/a.png"} {
		if strings.Contains(lower, bad) {
			t.Fatalf("sanitized HTML retained %q in %q", bad, got)
		}
	}
	if !strings.Contains(got, "<p>safe</p>") {
		t.Fatalf("sanitizer lost safe content: %q", got)
	}
}

func TestSanitizeHTMLProtectsTargetBlankLinks(t *testing.T) {
	got := sanitizeHTML(`<a href="https://example.com" target="_blank">safe</a>`)
	lower := strings.ToLower(got)
	for _, required := range []string{"target=\"_blank\"", "noopener", "noreferrer", "nofollow"} {
		if !strings.Contains(lower, required) {
			t.Fatalf("sanitized link missing %q: %q", required, got)
		}
	}
}

func TestSanitizeHTMLRejectsCredentialedAndAmbiguousURLs(t *testing.T) {
	input := `<a href="https://user:pass@example.com/path">credentialed</a><img src="https://user@example.com/image.png"><a href="https://example.com\@evil.test/path">ambiguous</a><a href="https://example.com/safe">safe</a>`
	got := sanitizeHTML(input)
	lower := strings.ToLower(got)
	for _, unsafe := range []string{"user:pass@", "user@example.com", "\\@evil.test"} {
		if strings.Contains(lower, unsafe) {
			t.Fatalf("sanitized HTML retained unsafe URL %q in %q", unsafe, got)
		}
	}
	if !strings.Contains(got, `href="https://example.com/safe"`) {
		t.Fatalf("sanitizer lost safe URL: %q", got)
	}
}

func TestSanitizeNoticeItemProtectsLegacyDatabaseContent(t *testing.T) {
	item := sanitizeNoticeItem(NoticeItem{
		ContentHTML: `<p onclick="alert(1)">legacy<script>alert(1)</script><a href="https://user:pass@example.com">bad</a></p>`,
		ID:          "1",
		Title:       "legacy notice",
	})
	lower := strings.ToLower(item.ContentHTML)
	for _, unsafe := range []string{"onclick", "<script", "user:pass@"} {
		if strings.Contains(lower, unsafe) {
			t.Fatalf("legacy notice output retained %q: %q", unsafe, item.ContentHTML)
		}
	}
	if !strings.Contains(item.ContentHTML, "legacy") || item.ID != "1" || item.Title != "legacy notice" {
		t.Fatalf("legacy notice sanitization lost safe data: %+v", item)
	}
}

func TestSanitizeHTMLIsIdempotent(t *testing.T) {
	once := sanitizeHTML(`<p>safe <a href="HTTPS://example.com/path" target="_blank">link</a><img src="/system-api/upload/notice-image/0123456789abcdef0123456789abcdef.png"></p>`)
	twice := sanitizeHTML(once)
	if once != twice {
		t.Fatalf("sanitizeHTML is not idempotent:\nonce=%q\ntwice=%q", once, twice)
	}
	if !strings.Contains(strings.ToLower(once), "https://example.com/path") {
		t.Fatalf("sanitizer rejected case-insensitive HTTPS URL: %q", once)
	}
}

func TestNormalizeNoticeRejectsOversizedHTML(t *testing.T) {
	_, err := normalizeNotice(SaveNoticeRequest{Title: "notice", ContentHTML: strings.Repeat("x", maxNoticeHTMLBytes+1)})
	if err == nil {
		t.Fatal("normalizeNotice accepted oversized HTML")
	}
}

func TestMakeSummaryUsesParsedTextAndEntities(t *testing.T) {
	if got := makeSummary(`<p>Hello &amp; <strong>BugPk-Api</strong></p>`); got != "Hello & BugPk-Api" {
		t.Fatalf("makeSummary() = %q", got)
	}
}

func TestNormalizeSiteURL(t *testing.T) {
	siteURL, normalized, err := normalizeSiteURL("www.example.com/path/")
	if err != nil {
		t.Fatal(err)
	}
	if siteURL != "https://www.example.com/path/" {
		t.Fatalf("unexpected siteURL %q", siteURL)
	}
	if normalized != "https://example.com/path" {
		t.Fatalf("unexpected normalized URL %q", normalized)
	}
}

func TestNormalizeSiteURLRejectsAmbiguousOrUnsafeHosts(t *testing.T) {
	for _, value := range []string{
		"https://user:pass@example.com/path",
		"https://example.com/path#login",
		"https://example.com\\@evil.test/path",
		"https://exa mple.com/path",
		"https://例子.测试/path",
		"https://-invalid.example/path",
		"https://example.com:70000/path",
	} {
		if _, _, err := normalizeSiteURL(value); err == nil {
			t.Fatalf("normalizeSiteURL accepted unsafe URL %q", value)
		}
	}
}

func TestNormalizeSiteURLCanonicalizesHostPortAndQuery(t *testing.T) {
	display, normalized, err := normalizeSiteURL("HTTPS://WWW.Example.COM.:443/path/?tracking=1")
	if err != nil {
		t.Fatal(err)
	}
	if display != "https://www.example.com/path/" {
		t.Fatalf("display URL = %q", display)
	}
	if normalized != "https://example.com/path" {
		t.Fatalf("normalized URL = %q", normalized)
	}
}

func TestNormalizeSiteConfigRejectsSilentTruncationAndBadEmail(t *testing.T) {
	if _, err := normalizeSiteConfig(SiteConfig{SiteName: strings.Repeat("站", 65)}, "/system-api"); err == nil {
		t.Fatal("oversized siteName should be rejected")
	}
	if _, err := normalizeSiteConfig(SiteConfig{SiteName: "BugPk-Api", ContactEmail: "not-an-email"}, "/system-api"); err == nil {
		t.Fatal("invalid contactEmail should be rejected")
	}
	if _, err := normalizeSiteConfig(SiteConfig{SiteName: "BugPk-Api", LoadingAnimationEnabled: 1}, "/system-api"); err == nil {
		t.Fatal("enabled custom loading animation without a URL should be rejected")
	}
}

func TestNormalizeSiteConfigPreservesBrandingFields(t *testing.T) {
	cfg, err := normalizeSiteConfig(SiteConfig{
		AuthorName:  " BugPk Team ",
		AuthorURL:   "https://example.com/about",
		GitHubURL:   "https://github.com/bugpk/bugpkapi",
		LicenseType: " MIT ",
		LicenseURL:  "https://github.com/bugpk/bugpkapi/blob/main/LICENSE",
		SiteName:    "BugPk-Api",
	}, "/system-api")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AuthorName != "BugPk Team" || cfg.LicenseType != "MIT" ||
		cfg.AuthorURL == "" || cfg.GitHubURL == "" || cfg.LicenseURL == "" {
		t.Fatalf("branding fields were not preserved: %+v", cfg)
	}
}

func TestNormalizeSiteConfigRejectsUnsafeBrandingURL(t *testing.T) {
	for _, field := range []SiteConfig{
		{SiteName: "BugPk-Api", GitHubURL: "javascript:alert(1)"},
		{SiteName: "BugPk-Api", AuthorURL: "https://user:pass@example.com"},
		{SiteName: "BugPk-Api", LicenseURL: "https://example.com/line\nbreak"},
	} {
		if _, err := normalizeSiteConfig(field, "/system-api"); err == nil {
			t.Fatalf("unsafe branding URL was accepted: %+v", field)
		}
	}
}

func TestNormalizeSiteConfigAcceptsUploadedLoadingAnimation(t *testing.T) {
	prefix := "/system-api/upload/loading-animation/"
	url := prefix + "0123456789abcdef0123456789abcdef.gif"
	cfg, err := normalizeSiteConfig(SiteConfig{
		SiteName:                "BugPk-Api",
		LoadingAnimationEnabled: 1,
		LoadingAnimationURL:     url,
	}, "/system-api")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LoadingAnimationURL != url || cfg.LoadingAnimationEnabled != 1 {
		t.Fatalf("unexpected loading animation config: %+v", cfg)
	}
}

func TestNormalizeSiteConfigValidatesLoadingAnimationPreset(t *testing.T) {
	cfg, err := normalizeSiteConfig(SiteConfig{SiteName: "BugPk-Api", LoadingAnimationPreset: " ORBIT "}, "/system-api")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LoadingAnimationPreset != "orbit" {
		t.Fatalf("preset=%q, want orbit", cfg.LoadingAnimationPreset)
	}
	if _, err := normalizeSiteConfig(SiteConfig{SiteName: "BugPk-Api", LoadingAnimationPreset: "unknown"}, "/system-api"); err == nil {
		t.Fatal("unknown loading animation preset was accepted")
	}
}

func TestNormalizeSiteConfigAcceptsAllSplashPresets(t *testing.T) {
	// 6 套内置预设 + 两个历史值，升级后不能让旧库的 pulse/bars 变成非法值。
	for _, preset := range []string{
		"forge", "gateway", "orbit", "scan", "rail", "dots", "pulse", "bars",
	} {
		cfg, err := normalizeSiteConfig(
			SiteConfig{SiteName: "BugPk-Api", LoadingAnimationPreset: preset},
			"/system-api",
		)
		if err != nil {
			t.Fatalf("preset %q rejected: %v", preset, err)
		}
		if cfg.LoadingAnimationPreset != preset {
			t.Fatalf("preset=%q, want %q", cfg.LoadingAnimationPreset, preset)
		}
	}
}

func TestNormalizeSiteConfigValidatesGlobalLoadingPreset(t *testing.T) {
	for _, preset := range []string{
		"bar", "ring", "skeleton", "packet", "logo", "dots",
	} {
		cfg, err := normalizeSiteConfig(
			SiteConfig{SiteName: "BugPk-Api", GlobalLoadingPreset: preset},
			"/system-api",
		)
		if err != nil {
			t.Fatalf("global preset %q rejected: %v", preset, err)
		}
		if cfg.GlobalLoadingPreset != preset {
			t.Fatalf("globalLoadingPreset=%q, want %q", cfg.GlobalLoadingPreset, preset)
		}
	}

	// 大小写与空白归一，空值回落默认 bar
	cfg, err := normalizeSiteConfig(
		SiteConfig{SiteName: "BugPk-Api", GlobalLoadingPreset: " SKELETON "},
		"/system-api",
	)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GlobalLoadingPreset != "skeleton" {
		t.Fatalf("globalLoadingPreset=%q, want skeleton", cfg.GlobalLoadingPreset)
	}

	cfg, err = normalizeSiteConfig(SiteConfig{SiteName: "BugPk-Api"}, "/system-api")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GlobalLoadingPreset != "bar" {
		t.Fatalf("default globalLoadingPreset=%q, want bar", cfg.GlobalLoadingPreset)
	}

	if _, err := normalizeSiteConfig(
		SiteConfig{SiteName: "BugPk-Api", GlobalLoadingPreset: "unknown"},
		"/system-api",
	); err == nil {
		t.Fatal("unknown global loading preset was accepted")
	}
}

func TestNormalizeSiteConfigAcceptsAnalyticsCodeWithoutTrimming(t *testing.T) {
	code := "\ufeff\n<script async src=\"https://analytics.example/script.js\"></script>\n"
	cfg, err := normalizeSiteConfig(SiteConfig{
		AnalyticsCode: code,
		SiteName:      "BugPk-Api",
	}, "/system-api")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AnalyticsCode != "\n<script async src=\"https://analytics.example/script.js\"></script>\n" {
		t.Fatalf("unexpected analytics code normalization: %q", cfg.AnalyticsCode)
	}
}

func TestNormalizeSiteConfigRejectsAnalyticsCodeWithNULOrOversize(t *testing.T) {
	if _, err := normalizeSiteConfig(SiteConfig{
		AnalyticsCode: "<script>\x00</script>",
		SiteName:      "BugPk-Api",
	}, "/system-api"); err == nil {
		t.Fatal("analyticsCode containing NUL should be rejected")
	}
	if _, err := normalizeSiteConfig(SiteConfig{
		AnalyticsCode: strings.Repeat("x", maxAnalyticsCodeBytes+1),
		SiteName:      "BugPk-Api",
	}, "/system-api"); err == nil {
		t.Fatal("oversized analyticsCode should be rejected")
	}
}

func TestPublicSiteSummaryOmitsAnalyticsCode(t *testing.T) {
	site := SiteConfig{
		AnalyticsCode: "<script>window.analytics = true;</script>",
		SiteName:      "BugPk-Api",
	}

	summary := publicSiteSummary(site)
	if summary.AnalyticsCode != "" {
		t.Fatalf("public summary must omit analytics code, got %q", summary.AnalyticsCode)
	}
	if summary.SiteName != site.SiteName {
		t.Fatalf("public summary lost site config fields: %+v", summary)
	}
}

func TestValidImageURLRequiresGeneratedLocalName(t *testing.T) {
	prefix := "/system-api/upload/site-logo/"
	if !validImageURL(prefix+"0123456789abcdef0123456789abcdef.webp", prefix) {
		t.Fatal("generated local image URL should be accepted")
	}
	for _, value := range []string{prefix + "../secret.png", prefix + "logo.png", "https://user:pass@example.com/logo.png"} {
		if validImageURL(value, prefix) {
			t.Fatalf("unsafe image URL accepted: %q", value)
		}
	}
}

func TestHomeOpenTrafficConditionExcludesAdminTestLogs(t *testing.T) {
	got := homeOpenTrafficCondition("l")
	if !strings.Contains(got, "l.charge_type") {
		t.Fatalf("expected aliased charge_type predicate, got %q", got)
	}
	if !strings.Contains(got, "<> 'ADMIN'") {
		t.Fatalf("expected ADMIN management-test logs to be excluded, got %q", got)
	}
	if strings.Contains(strings.ToLower(got), "coalesce(") {
		t.Fatalf("open traffic predicate should keep charge_type indexable, got %q", got)
	}
	if strings.ContainsAny(got, ";?") {
		t.Fatalf("home open traffic predicate should stay a static SQL fragment, got %q", got)
	}

	plain := homeOpenTrafficCondition("")
	if strings.Contains(plain, "l.charge_type") || !strings.Contains(plain, "charge_type") {
		t.Fatalf("unexpected unaliased predicate: %q", plain)
	}
}
