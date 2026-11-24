package systemsettings

import (
	"context"
	"encoding/json"
	"net"
	"regexp"
	"strings"

	"bugpk-api/internal/domain/rbac"
	apperrors "bugpk-api/internal/pkg/errors"

	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

// Captcha provider/scene constants mirror the values used by the auth domain
// (bp_captcha_provider_config.provider_key) and the seed rows in migration
// 000001. Kept local to avoid a cross-domain import.
const (
	captchaProviderPOW        = "pow"
	captchaProviderGeeTest4   = "geetest4"
	captchaProviderCloudflare = "cloudflare"
	captchaSceneDefault       = "default"
	defaultCaptchaDifficulty  = 3
	defaultCaptchaTimeoutMS   = 3000
	defaultCaptchaPolicy      = "deny"
)

var captchaScenes = []string{
	"default",
	"login",
	"register",
	"register_email",
	"register_mobile",
	"password_reset",
	"change_password",
	"friend_link_apply",
}

var captchaProviderOptions = []CaptchaProviderOption{
	{Key: captchaProviderPOW, Name: "POW（工作量证明）", Description: "默认验证方式，无需第三方账号"},
	{Key: captchaProviderGeeTest4, Name: "极验 4.0", Description: "官方 Web SDK + 服务端二次校验，需极验 captchaId/apiKey"},
	{Key: captchaProviderCloudflare, Name: "Cloudflare Turnstile", Description: "官方 Widget + Siteverify，需 Cloudflare siteKey/secretKey"},
}

var captchaCustomFieldPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// CaptchaProviderOption describes one selectable provider for the admin page.
type CaptchaProviderOption struct {
	Key         string `json:"key"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// CaptchaSceneConfig is the editable config of one scene.
type CaptchaSceneConfig struct {
	Scene    string `json:"scene"`
	Provider string `json:"provider"`
	Enabled  int    `json:"enabled"`
	// Inherited is true when the scene has no provider of its own and follows
	// the `default` scene. The scene switch stays independent of inheritance.
	Inherited     bool   `json:"inherited,omitempty"`
	Difficulty    int    `json:"difficulty"`
	CaptchaID     string `json:"captchaId"`
	APIKey        string `json:"apiKey"`
	SiteKey       string `json:"siteKey"`
	SecretKey     string `json:"secretKey"`
	Product       string `json:"product,omitempty"`
	Language      string `json:"language,omitempty"`
	Action        string `json:"action,omitempty"`
	Hostname      string `json:"hostname,omitempty"`
	CData         string `json:"cdata,omitempty"`
	TimeoutMS     int    `json:"timeoutMs"`
	FailurePolicy string `json:"failurePolicy"`
}

// CaptchaConfigResult is returned by GET /system/captcha-config.
type CaptchaConfigResult struct {
	GlobalEnabled bool                          `json:"globalEnabled"`
	Scenes        []string                      `json:"scenes"`
	EnabledScenes []string                      `json:"enabledScenes"`
	Providers     []CaptchaProviderOption       `json:"providers"`
	Configs       map[string]CaptchaSceneConfig `json:"configs"`
}

type SaveCaptchaGlobalRequest struct {
	Enabled bool `json:"enabled"`
}

// SaveCaptchaConfigRequest is accepted by PUT /system/captcha-config.
type SaveCaptchaConfigRequest struct {
	Scene         string `json:"scene"`
	Provider      string `json:"provider"`
	Enabled       int    `json:"enabled"`
	Difficulty    int    `json:"difficulty"`
	CaptchaID     string `json:"captchaId"`
	APIKey        string `json:"apiKey"`
	SiteKey       string `json:"siteKey"`
	SecretKey     string `json:"secretKey"`
	Product       string `json:"product"`
	Language      string `json:"language"`
	Action        string `json:"action"`
	Hostname      string `json:"hostname"`
	CData         string `json:"cdata"`
	TimeoutMS     int    `json:"timeoutMs"`
	FailurePolicy string `json:"failurePolicy"`
}

// SaveCaptchaScenesRequest updates the set of authentication scenes that
// require a captcha. It deliberately does not carry provider credentials.
type SaveCaptchaScenesRequest struct {
	Scenes []string `json:"scenes"`
}

// CaptchaConfig returns the full captcha configuration for the admin page.
func (s *Service) CaptchaConfig(ctx context.Context, p rbac.Principal) (CaptchaConfigResult, error) {
	if err := s.authz.Require(ctx, p, "system:captcha-config:view"); err != nil {
		return CaptchaConfigResult{}, err
	}
	globalEnabled, err := s.repo.CaptchaGlobalEnabled(ctx)
	if err != nil {
		return CaptchaConfigResult{}, err
	}
	rows, err := s.repo.CaptchaProviderConfigs(ctx)
	if err != nil {
		return CaptchaConfigResult{}, mapConfigSecretErr(err)
	}
	result := CaptchaConfigResult{
		GlobalEnabled: globalEnabled,
		Scenes:        append([]string(nil), captchaScenes...),
		EnabledScenes: make([]string, 0, len(captchaScenes)),
		Providers:     append([]CaptchaProviderOption(nil), captchaProviderOptions...),
		Configs:       make(map[string]CaptchaSceneConfig, len(captchaScenes)),
	}
	for _, scene := range captchaScenes {
		cfg := s.activeSceneConfig(rows, scene)
		result.Configs[scene] = cfg
		if cfg.Enabled == 1 {
			result.EnabledScenes = append(result.EnabledScenes, scene)
		}
	}
	return result, nil
}

func (s *Service) SaveCaptchaGlobal(ctx context.Context, p rbac.Principal, req SaveCaptchaGlobalRequest) error {
	if err := s.authz.Require(ctx, p, "system:captcha-config:update"); err != nil {
		return err
	}
	return s.repo.SaveCaptchaGlobalEnabled(ctx, req.Enabled)
}

// SaveCaptchaScenes updates all scene switches in one transaction while
// preserving each scene's selected provider and encrypted credentials.
func (s *Service) SaveCaptchaScenes(ctx context.Context, p rbac.Principal, req SaveCaptchaScenesRequest) error {
	if err := s.authz.Require(ctx, p, "system:captcha-config:update"); err != nil {
		return err
	}
	enabledScenes, err := normalizeCaptchaSceneSelection(req.Scenes)
	if err != nil {
		return err
	}
	rows, err := s.repo.CaptchaProviderConfigs(ctx)
	if err != nil {
		return mapConfigSecretErr(err)
	}
	for _, scene := range enabledScenes {
		row, ok, _ := selectCaptchaProviderRow(rows, scene)
		if !ok {
			continue
		}
		cfg := s.toSceneConfig(row)
		cfg.Enabled = 1
		// toSceneConfig masks values for the admin response; validation here
		// operates on the decrypted repository value instead.
		switch cfg.Provider {
		case captchaProviderGeeTest4:
			cfg.APIKey = row.SecretRef
		case captchaProviderCloudflare:
			cfg.SecretKey = row.SecretRef
		}
		if err := validateEnabledCaptchaCredentials(cfg); err != nil {
			return err
		}
	}
	return mapConfigSecretErr(s.repo.SaveCaptchaSceneStates(ctx, enabledScenes))
}

// SaveCaptchaConfig validates and persists the captcha provider for one scene.
func (s *Service) SaveCaptchaConfig(ctx context.Context, p rbac.Principal, req SaveCaptchaConfigRequest) error {
	if err := s.authz.Require(ctx, p, "system:captcha-config:update"); err != nil {
		return err
	}
	cfg, err := normalizeCaptchaConfig(req)
	if err != nil {
		return err
	}
	current, err := s.repo.CaptchaProviderConfigs(ctx)
	if err != nil {
		return mapConfigSecretErr(err)
	}
	// Keep masked secrets (******) untouched by reusing the stored value.
	cfg.APIKey = preservedSecret(cfg.APIKey, currentCaptchaSecret(current, cfg.Scene, "apiKey"))
	cfg.SecretKey = preservedSecret(cfg.SecretKey, currentCaptchaSecret(current, cfg.Scene, "secretKey"))
	if err := validateEnabledCaptchaCredentials(cfg); err != nil {
		return err
	}
	return mapConfigSecretErr(s.repo.SaveCaptchaProviderConfig(ctx, cfg))
}

// activeSceneConfig resolves the selected provider and scene switch. A
// provider the administrator configured for the scene wins; otherwise the
// `default` scene is inherited, while the scene's own enable/disable switch
// always stays with the scene.
func (s *Service) activeSceneConfig(rows []CaptchaProviderRow, scene string) CaptchaSceneConfig {
	picked, ok, inherited := selectCaptchaProviderRow(rows, scene)
	if !ok {
		return CaptchaSceneConfig{
			Scene:         scene,
			Provider:      captchaProviderPOW,
			Enabled:       1,
			Difficulty:    defaultCaptchaDifficulty,
			TimeoutMS:     defaultCaptchaTimeoutMS,
			FailurePolicy: defaultCaptchaPolicy,
		}
	}
	cfg := s.toSceneConfig(picked)
	cfg.Scene = scene
	cfg.Inherited = inherited
	if inherited {
		if sceneEnabled, found := sceneSwitchValue(rows, scene); found {
			cfg.Enabled = boolInt(sceneEnabled)
		}
	}
	return cfg
}

// selectCaptchaProviderRow resolves the effective provider row of a scene.
// Precedence: the scene's own row when the administrator configured it
// explicitly, then the `default` scene's explicit row (inherited), then an
// enabled scene seed row, then any scene row, then any `default` row. The
// third return value reports whether the picked row belongs to a different
// scene than the one requested.
func selectCaptchaProviderRow(rows []CaptchaProviderRow, scene string) (CaptchaProviderRow, bool, bool) {
	var sceneExplicit, defaultExplicit, sceneEnabled, sceneAny, defaultAny CaptchaProviderRow
	for _, row := range rows {
		switch row.Scene {
		case scene:
			if sceneAny.ProviderKey == "" {
				sceneAny = row
			}
			if row.Enabled != 1 {
				continue
			}
			if sceneEnabled.ProviderKey == "" {
				sceneEnabled = row
			}
			if row.Explicit == 1 && sceneExplicit.ProviderKey == "" {
				sceneExplicit = row
			}
		case captchaSceneDefault:
			if defaultAny.ProviderKey == "" {
				defaultAny = row
			}
			if row.Enabled == 1 && row.Explicit == 1 && defaultExplicit.ProviderKey == "" {
				defaultExplicit = row
			}
		}
	}
	for _, candidate := range []CaptchaProviderRow{sceneExplicit, defaultExplicit, sceneEnabled, sceneAny, defaultAny} {
		if candidate.ProviderKey != "" {
			return candidate, true, candidate.Scene != scene
		}
	}
	return CaptchaProviderRow{}, false, false
}

// sceneSwitchValue returns the scene-level enable flag recorded on any row of
// the scene, so an inherited provider never drags the default scene's switch
// across.
func sceneSwitchValue(rows []CaptchaProviderRow, scene string) (int, bool) {
	for _, row := range rows {
		if row.Scene == scene {
			return row.SceneEnabled, true
		}
	}
	return 0, false
}

func (s *Service) toSceneConfig(row CaptchaProviderRow) CaptchaSceneConfig {
	cfg := CaptchaSceneConfig{
		Scene:         row.Scene,
		Provider:      row.ProviderKey,
		Enabled:       boolInt(row.SceneEnabled),
		TimeoutMS:     row.TimeoutMS,
		FailurePolicy: row.FailurePolicy,
	}
	if cfg.TimeoutMS <= 0 {
		cfg.TimeoutMS = defaultCaptchaTimeoutMS
	}
	// Captcha validation is always fail-closed. Preserve the field in the API
	// for database compatibility, but never advertise an insecure allow policy.
	cfg.FailurePolicy = defaultCaptchaPolicy
	var extra map[string]any
	if err := json.Unmarshal([]byte(row.ExtraConfig), &extra); err != nil {
		extra = nil
	}
	cfg.Difficulty = intFieldValue(extra, "difficulty")
	cfg.CaptchaID = stringFieldValue(extra, "captchaId")
	cfg.SiteKey = stringFieldValue(extra, "siteKey")
	cfg.Product = stringFieldValue(extra, "product")
	cfg.Language = stringFieldValue(extra, "language")
	cfg.Action = stringFieldValue(extra, "action")
	cfg.Hostname = stringFieldValue(extra, "hostname")
	cfg.CData = stringFieldValue(extra, "cdata")
	if strings.TrimSpace(row.SecretRef) != "" {
		switch cfg.Provider {
		case captchaProviderGeeTest4:
			cfg.APIKey = row.SecretRef
		case captchaProviderCloudflare:
			cfg.SecretKey = row.SecretRef
		}
	}
	if cfg.APIKey != "" {
		cfg.APIKey = secretMask
	}
	if cfg.SecretKey != "" {
		cfg.SecretKey = secretMask
	}
	return cfg
}

func normalizeCaptchaSceneSelection(values []string) ([]string, error) {
	selected := make(map[string]struct{}, len(values))
	for _, value := range values {
		scene := strings.TrimSpace(value)
		if !containsString(captchaScenes, scene) {
			return nil, apperrors.New(consts.StatusBadRequest, "验证码场景不合法")
		}
		selected[scene] = struct{}{}
	}
	result := make([]string, 0, len(selected))
	for _, scene := range captchaScenes {
		if _, ok := selected[scene]; ok {
			result = append(result, scene)
		}
	}
	return result, nil
}

func normalizeCaptchaConfig(req SaveCaptchaConfigRequest) (CaptchaSceneConfig, error) {
	scene := strings.TrimSpace(req.Scene)
	if !containsString(captchaScenes, scene) {
		return CaptchaSceneConfig{}, apperrors.New(consts.StatusBadRequest, "验证码场景不合法")
	}
	provider := strings.TrimSpace(req.Provider)
	if !containsProviderOption(captchaProviderOptions, provider) {
		return CaptchaSceneConfig{}, apperrors.New(consts.StatusBadRequest, "验证码 Provider 不合法")
	}
	cfg := CaptchaSceneConfig{
		Scene:         scene,
		Provider:      provider,
		Enabled:       boolInt(req.Enabled),
		Difficulty:    clampInt(req.Difficulty, 1, 6, defaultCaptchaDifficulty),
		CaptchaID:     strings.TrimSpace(req.CaptchaID),
		APIKey:        strings.TrimSpace(req.APIKey),
		SiteKey:       strings.TrimSpace(req.SiteKey),
		SecretKey:     strings.TrimSpace(req.SecretKey),
		Product:       strings.ToLower(strings.TrimSpace(req.Product)),
		Language:      strings.TrimSpace(req.Language),
		Action:        strings.TrimSpace(req.Action),
		Hostname:      strings.TrimSpace(req.Hostname),
		CData:         strings.TrimSpace(req.CData),
		TimeoutMS:     clampInt(req.TimeoutMS, 100, 30000, defaultCaptchaTimeoutMS),
		FailurePolicy: defaultCaptchaPolicy,
	}
	if cfg.Product == "" {
		cfg.Product = "float"
	}
	if cfg.Product != "bind" && cfg.Product != "float" && cfg.Product != "popup" {
		return CaptchaSceneConfig{}, apperrors.New(consts.StatusBadRequest, "GeeTest product must be bind, float, or popup")
	}
	// secret_ref is varchar(255); 128-byte plaintext remains safely below it
	// after the AES-GCM nonce/tag and enc:v1 base64 envelope are added.
	if len([]rune(cfg.CaptchaID)) > 128 || len([]byte(cfg.APIKey)) > 128 || len([]byte(cfg.SiteKey)) > 255 || len([]byte(cfg.SecretKey)) > 128 {
		return CaptchaSceneConfig{}, apperrors.New(consts.StatusBadRequest, "captcha key is too long")
	}
	if len([]rune(cfg.Language)) > 32 || len(cfg.Action) > 32 || len(cfg.Hostname) > 253 || len(cfg.CData) > 128 {
		return CaptchaSceneConfig{}, apperrors.New(consts.StatusBadRequest, "captcha option is too long")
	}
	if cfg.Action != "" && !captchaCustomFieldPattern.MatchString(cfg.Action) {
		return CaptchaSceneConfig{}, apperrors.New(consts.StatusBadRequest, "Cloudflare action 仅支持字母、数字、下划线和短横线")
	}
	if cfg.CData != "" && !captchaCustomFieldPattern.MatchString(cfg.CData) {
		return CaptchaSceneConfig{}, apperrors.New(consts.StatusBadRequest, "Cloudflare cData 前缀仅支持字母、数字、下划线和短横线")
	}
	if cfg.Hostname != "" {
		cfg.Hostname = normalizeCaptchaHostname(cfg.Hostname)
		if cfg.Hostname == "" {
			return CaptchaSceneConfig{}, apperrors.New(consts.StatusBadRequest, "Cloudflare hostname 格式不合法")
		}
	}
	if err := validateEnabledCaptchaCredentials(cfg); err != nil {
		return CaptchaSceneConfig{}, err
	}
	return cfg, nil
}

func validateEnabledCaptchaCredentials(cfg CaptchaSceneConfig) error {
	if cfg.Enabled != 1 {
		return nil
	}
	switch cfg.Provider {
	case captchaProviderGeeTest4:
		if cfg.CaptchaID == "" || cfg.APIKey == "" {
			return apperrors.New(consts.StatusBadRequest, "启用极验4.0需填写 captchaId 和 apiKey")
		}
	case captchaProviderCloudflare:
		if cfg.SiteKey == "" || cfg.SecretKey == "" {
			return apperrors.New(consts.StatusBadRequest, "启用 Cloudflare Turnstile 需填写 siteKey 和 secretKey")
		}
	}
	return nil
}

func normalizeCaptchaHostname(value string) string {
	value = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(value), "."))
	if value == "" || len(value) > 253 || strings.ContainsAny(value, "/\\:@ \t\r\n") {
		return ""
	}
	if net.ParseIP(value) != nil || value == "localhost" {
		return value
	}
	for _, label := range strings.Split(value, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return ""
		}
		for _, char := range label {
			if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '-' {
				return ""
			}
		}
	}
	return value
}

// currentCaptchaSecret returns the stored (decrypted) secret for a scene and
// kind so masked values survive an unrelated update.
func currentCaptchaSecret(rows []CaptchaProviderRow, scene, kind string) string {
	provider := captchaProviderCloudflare
	if kind == "apiKey" {
		provider = captchaProviderGeeTest4
	}
	for _, candidate := range []struct {
		scene       string
		enabledOnly bool
	}{
		{scene: scene, enabledOnly: true},
		{scene: "default", enabledOnly: true},
		{scene: scene, enabledOnly: false},
		{scene: "default", enabledOnly: false},
	} {
		for _, row := range rows {
			if row.Scene != candidate.scene || row.ProviderKey != provider || strings.TrimSpace(row.SecretRef) == "" {
				continue
			}
			if candidate.enabledOnly && row.Enabled != 1 {
				continue
			}
			return row.SecretRef
		}
	}
	return ""
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func containsProviderOption(values []CaptchaProviderOption, target string) bool {
	for _, value := range values {
		if value.Key == target {
			return true
		}
	}
	return false
}

func stringFieldValue(values map[string]any, key string) string {
	if values == nil {
		return ""
	}
	value, _ := values[key].(string)
	return strings.TrimSpace(value)
}

func intFieldValue(values map[string]any, key string) int {
	if values == nil {
		return 0
	}
	value, _ := values[key].(float64)
	return int(value)
}
