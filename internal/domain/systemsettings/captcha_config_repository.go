package systemsettings

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
)

// CaptchaProviderRow is one row of bp_captcha_provider_config. SecretRef holds
// the decrypted third-party secret (apiKey for geetest4, secretKey for
// cloudflare) so the service can mask it before returning to the admin page.
type CaptchaProviderRow struct {
	ProviderKey  string
	Scene        string
	Enabled      int
	SceneEnabled int
	// Explicit is 1 when the administrator chose this provider for the scene
	// and 0 for the stock seed rows that merely inherit from `default`.
	Explicit      int
	SiteKey       string
	SecretRef     string
	TimeoutMS     int
	FailurePolicy string
	ExtraConfig   string
}

func (r *Repository) CaptchaGlobalEnabled(ctx context.Context) (bool, error) {
	var enabled int
	err := r.db.QueryRowContext(ctx, `
		select enabled
		from bp_captcha_global_config
		where id = 1
	`).Scan(&enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return enabled == 1, nil
}

func (r *Repository) SaveCaptchaGlobalEnabled(ctx context.Context, enabled bool) error {
	value := 0
	if enabled {
		value = 1
	}
	_, err := r.db.ExecContext(ctx, `
		insert into bp_captcha_global_config(id, enabled)
		values (1, ?)
		on duplicate key update enabled = values(enabled)
	`, value)
	return err
}

// CaptchaProviderConfigs returns every captcha provider row ordered by id.
func (r *Repository) CaptchaProviderConfigs(ctx context.Context) ([]CaptchaProviderRow, error) {
	rows, err := r.db.QueryContext(ctx, `
		select provider_key, scene, enabled,
		       coalesce(scene_enabled, 1),
		       coalesce(explicit_provider, 0),
		       coalesce(site_key, ''),
		       coalesce(secret_ref, ''),
		       coalesce(timeout_ms, 0),
		       coalesce(failure_policy, ''),
		       coalesce(cast(extra_config as char), '')
		from bp_captcha_provider_config
		order by id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []CaptchaProviderRow
	for rows.Next() {
		var row CaptchaProviderRow
		if err := rows.Scan(&row.ProviderKey, &row.Scene, &row.Enabled, &row.SceneEnabled, &row.Explicit, &row.SiteKey, &row.SecretRef, &row.TimeoutMS, &row.FailurePolicy, &row.ExtraConfig); err != nil {
			return nil, err
		}
		row.SecretRef, err = r.decryptCaptchaSecret(row.ProviderKey, row.SecretRef)
		if err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func (r *Repository) decryptCaptchaSecret(providerKey, stored string) (string, error) {
	stored = strings.TrimSpace(stored)
	if stored == "" {
		return "", nil
	}
	return r.cipher.DecryptCompatible(stored)
}

// SaveCaptchaProviderConfig persists the selected provider for one scene and
// disables every other provider of that scene so the switch takes effect.
// Third-party secrets are encrypted with the system config cipher.
func (r *Repository) SaveCaptchaProviderConfig(ctx context.Context, cfg CaptchaSceneConfig) error {
	extra := map[string]any{"difficulty": cfg.Difficulty}
	siteKey := ""
	secretRef := ""
	switch cfg.Provider {
	case captchaProviderGeeTest4:
		extra["captchaId"] = cfg.CaptchaID
		extra["product"] = cfg.Product
		if strings.TrimSpace(cfg.Language) != "" {
			extra["language"] = cfg.Language
		}
		siteKey = cfg.CaptchaID
		if strings.TrimSpace(cfg.APIKey) != "" {
			encrypted, err := r.cipher.Encrypt(cfg.APIKey)
			if err != nil {
				return err
			}
			secretRef = encrypted
		}
	case captchaProviderCloudflare:
		extra["siteKey"] = cfg.SiteKey
		if strings.TrimSpace(cfg.Action) != "" {
			extra["action"] = cfg.Action
		}
		if strings.TrimSpace(cfg.Hostname) != "" {
			extra["hostname"] = cfg.Hostname
		}
		if strings.TrimSpace(cfg.CData) != "" {
			extra["cdata"] = cfg.CData
		}
		siteKey = cfg.SiteKey
		if strings.TrimSpace(cfg.SecretKey) != "" {
			encrypted, err := r.cipher.Encrypt(cfg.SecretKey)
			if err != nil {
				return err
			}
			secretRef = encrypted
		}
	}
	extraJSON, err := json.Marshal(extra)
	if err != nil {
		return err
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `
		update bp_captcha_provider_config
		set enabled = 0, scene_enabled = ?
		where scene = ?
	`, cfg.Enabled, cfg.Scene); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		insert into bp_captcha_provider_config(
			provider_key, scene, enabled, scene_enabled, explicit_provider,
			site_key, secret_ref, timeout_ms, failure_policy, extra_config
		)
		values (?, ?, 1, ?, 1, ?, ?, ?, ?, ?)
		on duplicate key update
			enabled = 1,
			scene_enabled = values(scene_enabled),
			explicit_provider = 1,
			site_key = values(site_key),
			secret_ref = values(secret_ref),
			timeout_ms = values(timeout_ms),
			failure_policy = values(failure_policy),
			extra_config = values(extra_config)
	`, cfg.Provider, cfg.Scene, cfg.Enabled, nullCaptchaString(siteKey), nullCaptchaString(secretRef), cfg.TimeoutMS, cfg.FailurePolicy, string(extraJSON)); err != nil {
		return err
	}
	return tx.Commit()
}

// SaveCaptchaSceneStates changes only the scene-level enable flags. Provider
// selection and all provider credentials remain untouched.
func (r *Repository) SaveCaptchaSceneStates(ctx context.Context, enabledScenes []string) error {
	enabled := make(map[string]struct{}, len(enabledScenes))
	for _, scene := range enabledScenes {
		enabled[scene] = struct{}{}
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	for _, scene := range captchaScenes {
		value := 0
		if _, ok := enabled[scene]; ok {
			value = 1
		}
		if _, err := tx.ExecContext(ctx, `
			update bp_captcha_provider_config
			set scene_enabled = ?
			where scene = ?
		`, value, scene); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func nullCaptchaString(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return strings.TrimSpace(value)
}
