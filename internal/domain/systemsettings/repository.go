package systemsettings

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"bugpk-api/internal/pkg/secretbox"
)

type Repository struct {
	cipher *secretbox.Cipher
	db     *sql.DB
}

func NewRepository(db *sql.DB, ciphers ...*secretbox.Cipher) *Repository {
	cipher := secretbox.New("")
	if len(ciphers) > 0 && ciphers[0] != nil {
		cipher = ciphers[0]
	}
	return &Repository{db: db, cipher: cipher}
}

func (r *Repository) MigrateLegacySecrets(ctx context.Context) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	type providerRow struct {
		channel     string
		extraConfig string
		provider    string
	}
	rows, err := tx.QueryContext(ctx, `
		select channel, provider_key, coalesce(cast(extra_config as char), '{}')
		from bp_message_provider_config
		where (channel='email' and provider_key='smtp')
		   or (channel='sms' and provider_key='aliyun')
		for update
	`)
	if err != nil {
		return err
	}
	var providers []providerRow
	for rows.Next() {
		var row providerRow
		if err := rows.Scan(&row.channel, &row.provider, &row.extraConfig); err != nil {
			_ = rows.Close()
			return err
		}
		providers = append(providers, row)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}

	for _, row := range providers {
		values := map[string]any{}
		if err := json.Unmarshal([]byte(row.extraConfig), &values); err != nil {
			return fmt.Errorf("decode %s/%s provider config: %w", row.channel, row.provider, err)
		}
		secretKey := "accessKeySecret"
		if row.channel == "email" {
			secretKey = "authCode"
		}
		stored := strings.TrimSpace(fmt.Sprintf("%v", values[secretKey]))
		migrated, changed, err := migrateStoredSecret(r.cipher, stored)
		if err != nil {
			return fmt.Errorf("migrate %s/%s provider secret: %w", row.channel, row.provider, err)
		}
		if !changed {
			continue
		}
		values[secretKey] = migrated
		encoded, err := json.Marshal(values)
		if err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `
			update bp_message_provider_config set extra_config=?
			where channel=? and provider_key=?
		`, string(encoded), row.channel, row.provider)
		if err != nil {
			return err
		}
		if err := requireAffected(result); err != nil {
			return err
		}
	}

	var qqAppKey string
	err = tx.QueryRowContext(ctx, `select app_key from bp_auth_qq_login_config where id=1 for update`).Scan(&qqAppKey)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil {
		migrated, changed, err := migrateStoredSecret(r.cipher, qqAppKey)
		if err != nil {
			return fmt.Errorf("migrate QQ AppKey: %w", err)
		}
		if changed {
			result, err := tx.ExecContext(ctx, `update bp_auth_qq_login_config set app_key=? where id=1`, migrated)
			if err != nil {
				return err
			}
			if err := requireAffected(result); err != nil {
				return err
			}
		}
	}

	return tx.Commit()
}

func migrateStoredSecret(cipher *secretbox.Cipher, stored string) (string, bool, error) {
	stored = strings.TrimSpace(stored)
	if stored == "" || stored == "<nil>" {
		return "", false, nil
	}
	if secretbox.IsEncrypted(stored) {
		_, err := cipher.DecryptCompatible(stored)
		return stored, false, err
	}
	migrated, err := cipher.Encrypt(stored)
	if err != nil {
		return "", false, err
	}
	return migrated, true, nil
}

func (r *Repository) RegisterOpenConfig(ctx context.Context) (RegisterOpenConfig, error) {
	var cfg RegisterOpenConfig
	err := r.db.QueryRowContext(ctx, `
		select cfg.register_enabled,
		       coalesce(cfg.default_role_id, 0),
		       coalesce(role_default.role_name, ''),
		       coalesce(role_default.qps_limit, cfg.default_user_qps),
		       cfg.default_user_qps,
		       register_gift_points,
		       verification_code_ip_minute_limit,
		       verification_code_ip_hour_limit,
		       verification_code_ip_day_limit,
		       register_ip_hour_limit,
		       register_ip_day_limit,
		       coalesce(register_ip_max_accounts, 0),
		       coalesce(new_account_day_limit, 0),
		       coalesce(register_device_day_limit, 0),
		       coalesce(register_ip_prefix_day_limit, 0),
		       coalesce(register_risk_enabled, 1),
		       coalesce(register_risk_shadow_mode, 0),
		       coalesce(register_risk_quarantine_score, 40),
		       coalesce(register_risk_reject_score, 75),
		       oauth_register_enabled,
		       oauth_register_mode_default,
		       oauth_register_ip_hour_limit,
		       oauth_register_ip_day_limit,
		       coalesce(oauth_register_ip_max_accounts, 0),
		       oauth_require_verified_email
		from bp_auth_register_config cfg
		left join bp_role role_default on role_default.id = cfg.default_role_id
		where cfg.id = 1
	`).Scan(
		&cfg.RegisterEnabled,
		&cfg.DefaultRoleID,
		&cfg.DefaultRoleName,
		&cfg.DefaultRoleQps,
		&cfg.DefaultUserQps,
		&cfg.RegisterGiftPoints,
		&cfg.VerificationCodeIPMinuteLimit,
		&cfg.VerificationCodeIPHourLimit,
		&cfg.VerificationCodeIPDayLimit,
		&cfg.RegisterIPHourLimit,
		&cfg.RegisterIPDayLimit,
		&cfg.RegisterIPMaxAccounts,
		&cfg.NewAccountDayLimit,
		&cfg.RegisterDeviceDayLimit,
		&cfg.RegisterIPPrefixDayLimit,
		&cfg.RegisterRiskEnabled,
		&cfg.RegisterRiskShadowMode,
		&cfg.RegisterRiskQuarantineScore,
		&cfg.RegisterRiskRejectScore,
		&cfg.OAuthRegisterEnabled,
		&cfg.OAuthRegisterModeDefault,
		&cfg.OAuthRegisterIPHourLimit,
		&cfg.OAuthRegisterIPDayLimit,
		&cfg.OAuthRegisterIPMaxAccounts,
		&cfg.OAuthRequireVerifiedEmail,
	)
	if errors.Is(err, sql.ErrNoRows) {
		cfg = RegisterOpenConfig{
			DefaultRoleName:               "普通用户",
			DefaultRoleQps:                1,
			DefaultUserQps:                1,
			RegisterEnabled:               1,
			RegisterGiftPoints:            0,
			RegisterIPHourLimit:           10,
			NewAccountDayLimit:            0,
			RegisterDeviceDayLimit:        5,
			RegisterIPPrefixDayLimit:      10,
			RegisterRiskEnabled:           1,
			RegisterRiskShadowMode:        1,
			RegisterRiskQuarantineScore:   40,
			RegisterRiskRejectScore:       75,
			OAuthRegisterEnabled:          1,
			OAuthRegisterModeDefault:      "confirm",
			OAuthRegisterIPDayLimit:       10,
			VerificationCodeIPMinuteLimit: 5,
			VerificationCodeIPHourLimit:   20,
			VerificationCodeIPDayLimit:    50,
		}
		return cfg, r.SaveRegisterOpenConfig(ctx, cfg)
	}
	return cfg, err
}

func (r *Repository) SaveRegisterOpenConfig(ctx context.Context, cfg RegisterOpenConfig) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	roleID := cfg.DefaultRoleID
	if roleID <= 0 {
		if err := tx.QueryRowContext(ctx, "select id from bp_role where role_key = 'user' limit 1").Scan(&roleID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrInvalidDefaultRegisterRole
			}
			return err
		}
	}
	var roleQPS int
	if err := tx.QueryRowContext(ctx, `
		select qps_limit
		from bp_role
		where id = ?
		  and status = 1
		  and self_register_enabled = 1
		  and role_key <> 'admin'
		for update
	`, roleID).Scan(&roleQPS); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrInvalidDefaultRegisterRole
		}
		return err
	}
	_, err = tx.ExecContext(ctx, `
		insert into bp_auth_register_config(
			id, register_enabled, default_role_id, default_user_qps, register_gift_points,
			verification_code_ip_minute_limit, verification_code_ip_hour_limit,
			verification_code_ip_day_limit, register_ip_hour_limit, register_ip_day_limit,
			register_ip_max_accounts, new_account_day_limit, register_device_day_limit,
			register_ip_prefix_day_limit, register_risk_enabled, register_risk_shadow_mode,
			register_risk_quarantine_score, register_risk_reject_score,
			oauth_register_enabled, oauth_register_mode_default,
			oauth_register_ip_hour_limit, oauth_register_ip_day_limit,
			oauth_register_ip_max_accounts, oauth_require_verified_email
		)
		values (1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		on duplicate key update
			register_enabled = values(register_enabled),
			default_role_id = values(default_role_id),
			default_user_qps = values(default_user_qps),
			register_gift_points = values(register_gift_points),
			verification_code_ip_minute_limit = values(verification_code_ip_minute_limit),
			verification_code_ip_hour_limit = values(verification_code_ip_hour_limit),
			verification_code_ip_day_limit = values(verification_code_ip_day_limit),
			register_ip_hour_limit = values(register_ip_hour_limit),
			register_ip_day_limit = values(register_ip_day_limit),
			register_ip_max_accounts = values(register_ip_max_accounts),
			new_account_day_limit = values(new_account_day_limit),
			register_device_day_limit = values(register_device_day_limit),
			register_ip_prefix_day_limit = values(register_ip_prefix_day_limit),
			register_risk_enabled = values(register_risk_enabled),
			register_risk_shadow_mode = values(register_risk_shadow_mode),
			register_risk_quarantine_score = values(register_risk_quarantine_score),
			register_risk_reject_score = values(register_risk_reject_score),
			oauth_register_enabled = values(oauth_register_enabled),
			oauth_register_mode_default = values(oauth_register_mode_default),
			oauth_register_ip_hour_limit = values(oauth_register_ip_hour_limit),
			oauth_register_ip_day_limit = values(oauth_register_ip_day_limit),
			oauth_register_ip_max_accounts = values(oauth_register_ip_max_accounts),
			oauth_require_verified_email = values(oauth_require_verified_email)
	`, cfg.RegisterEnabled, roleID, roleQPS, cfg.RegisterGiftPoints, cfg.VerificationCodeIPMinuteLimit, cfg.VerificationCodeIPHourLimit, cfg.VerificationCodeIPDayLimit, cfg.RegisterIPHourLimit, cfg.RegisterIPDayLimit, cfg.RegisterIPMaxAccounts, cfg.NewAccountDayLimit, cfg.RegisterDeviceDayLimit, cfg.RegisterIPPrefixDayLimit, cfg.RegisterRiskEnabled, cfg.RegisterRiskShadowMode, cfg.RegisterRiskQuarantineScore, cfg.RegisterRiskRejectScore, cfg.OAuthRegisterEnabled, cfg.OAuthRegisterModeDefault, cfg.OAuthRegisterIPHourLimit, cfg.OAuthRegisterIPDayLimit, cfg.OAuthRegisterIPMaxAccounts, cfg.OAuthRequireVerifiedEmail)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// EmailDomainRules loads the registration domain policy.
func (r *Repository) EmailDomainRules(ctx context.Context) (EmailDomainRules, error) {
	rows, err := r.db.QueryContext(ctx, `
		select rule_kind, domain
		from bp_email_domain_rule
		order by domain
	`)
	if err != nil {
		return EmailDomainRules{}, err
	}
	defer rows.Close()

	var result EmailDomainRules
	for rows.Next() {
		var kind, domain string
		if err := rows.Scan(&kind, &domain); err != nil {
			return EmailDomainRules{}, err
		}
		switch kind {
		case "whitelist":
			result.Whitelist = append(result.Whitelist, domain)
		case "blacklist":
			result.Blacklist = append(result.Blacklist, domain)
		}
	}
	return result, rows.Err()
}

// SaveEmailDomainRules replaces the whole registration domain policy in one
// transaction: delete all rows, then insert both lists.
func (r *Repository) SaveEmailDomainRules(ctx context.Context, rules EmailDomainRules) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `delete from bp_email_domain_rule`); err != nil {
		return err
	}
	for kind, domains := range map[string][]string{"whitelist": rules.Whitelist, "blacklist": rules.Blacklist} {
		for _, domain := range domains {
			if _, err := tx.ExecContext(ctx, `
				insert into bp_email_domain_rule(rule_kind, domain) values (?, ?)
			`, kind, domain); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

var ErrInvalidDefaultRegisterRole = errors.New("invalid default registration role")

func (r *Repository) EmailConfig(ctx context.Context) (EmailConfig, error) {
	cfg := EmailConfig{SMTPPort: 587}
	var extraText string
	err := r.db.QueryRowContext(ctx, `
		select enabled, coalesce(cast(extra_config as char), '{}')
		from bp_message_provider_config
		where channel = 'email' and provider_key = 'smtp'
		limit 1
	`).Scan(&cfg.Enabled, &extraText)
	if errors.Is(err, sql.ErrNoRows) {
		return cfg, nil
	}
	if err != nil {
		return EmailConfig{}, err
	}
	var extra struct {
		AuthCode    string `json:"authCode"`
		SenderEmail string `json:"senderEmail"`
		SenderName  string `json:"senderName"`
		SMTPTLS     *bool  `json:"smtpTLS"`
		SMTPPort    int    `json:"smtpPort"`
		SMTPServer  string `json:"smtpServer"`
	}
	if err := json.Unmarshal([]byte(extraText), &extra); err != nil {
		return EmailConfig{}, err
	}
	cfg.AuthCode, err = r.cipher.DecryptCompatible(extra.AuthCode)
	if err != nil {
		return EmailConfig{}, err
	}
	cfg.SenderEmail = extra.SenderEmail
	cfg.SenderName = extra.SenderName
	cfg.SMTPPort = extra.SMTPPort
	cfg.SMTPServer = extra.SMTPServer
	cfg.SMTPTLS = defaultSMTPTLS(cfg.SMTPPort)
	if extra.SMTPTLS != nil {
		cfg.SMTPTLS = *extra.SMTPTLS
	}
	if cfg.SMTPPort <= 0 {
		cfg.SMTPPort = 587
	}
	return cfg, nil
}

func (r *Repository) SiteName(ctx context.Context) (string, error) {
	var siteName string
	err := r.db.QueryRowContext(ctx, `
		select coalesce(site_name, '')
		from bp_site_config
		where id = 1
	`).Scan(&siteName)
	if errors.Is(err, sql.ErrNoRows) {
		return defaultSettingsSiteName, nil
	}
	if err != nil {
		return "", err
	}
	return normalizedSettingsSiteName(siteName), nil
}

func (r *Repository) SaveEmailConfig(ctx context.Context, cfg EmailConfig) error {
	authCode, err := r.cipher.Encrypt(cfg.AuthCode)
	if err != nil {
		return err
	}
	extra, err := json.Marshal(map[string]any{
		"authCode":    authCode,
		"senderEmail": cfg.SenderEmail,
		"senderName":  cfg.SenderName,
		"smtpPort":    cfg.SMTPPort,
		"smtpServer":  cfg.SMTPServer,
		"smtpTLS":     cfg.SMTPTLS,
	})
	if err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `
		insert into bp_message_provider_config(channel, provider_key, enabled, config_ref, extra_config)
		values ('email', 'smtp', ?, 'DB', ?)
		on duplicate key update
			enabled = values(enabled),
			config_ref = values(config_ref),
			extra_config = values(extra_config)
	`, cfg.Enabled, string(extra)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		insert into bp_auth_register_config(id, email_register_enabled)
		values (1, ?)
		on duplicate key update email_register_enabled = values(email_register_enabled)
	`, cfg.Enabled); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Repository) MobileConfig(ctx context.Context) (MobileConfig, error) {
	cfg := MobileConfig{
		Endpoint: "dypnsapi.aliyuncs.com",
		Provider: "aliyun",
		Region:   "cn-hangzhou",
	}
	var extraText string
	err := r.db.QueryRowContext(ctx, `
		select enabled, coalesce(cast(extra_config as char), '{}')
		from bp_message_provider_config
		where channel = 'sms' and provider_key = 'aliyun'
		limit 1
	`).Scan(&cfg.Enabled, &extraText)
	if errors.Is(err, sql.ErrNoRows) {
		return cfg, nil
	}
	if err != nil {
		return MobileConfig{}, err
	}
	var extra struct {
		AccessKeyID     string `json:"accessKeyId"`
		AccessKeySecret string `json:"accessKeySecret"`
		Endpoint        string `json:"endpoint"`
		Region          string `json:"region"`
		SignName        string `json:"signName"`
		TemplateID      string `json:"templateId"`
	}
	if err := json.Unmarshal([]byte(extraText), &extra); err != nil {
		return MobileConfig{}, err
	}
	cfg.AccessKeyID = extra.AccessKeyID
	cfg.AccessKeySecret, err = r.cipher.DecryptCompatible(extra.AccessKeySecret)
	if err != nil {
		return MobileConfig{}, err
	}
	cfg.Endpoint = extra.Endpoint
	cfg.Region = extra.Region
	cfg.SignName = extra.SignName
	cfg.TemplateID = extra.TemplateID
	if cfg.Endpoint == "" {
		cfg.Endpoint = "dypnsapi.aliyuncs.com"
	}
	if cfg.Region == "" {
		cfg.Region = "cn-hangzhou"
	}
	return cfg, nil
}

func (r *Repository) SaveMobileConfig(ctx context.Context, cfg MobileConfig) error {
	accessKeySecret, err := r.cipher.Encrypt(cfg.AccessKeySecret)
	if err != nil {
		return err
	}
	extra, err := json.Marshal(map[string]any{
		"accessKeyId":     cfg.AccessKeyID,
		"accessKeySecret": accessKeySecret,
		"endpoint":        cfg.Endpoint,
		"officialSdkOnly": true,
		"region":          cfg.Region,
		"signName":        cfg.SignName,
		"templateId":      cfg.TemplateID,
	})
	if err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `
		insert into bp_message_provider_config(channel, provider_key, enabled, config_ref, extra_config)
		values ('sms', 'aliyun', ?, 'DB', ?)
		on duplicate key update
			enabled = values(enabled),
			config_ref = values(config_ref),
			extra_config = values(extra_config)
	`, cfg.Enabled, string(extra)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		insert into bp_auth_register_config(id, mobile_register_enabled)
		values (1, ?)
		on duplicate key update mobile_register_enabled = values(mobile_register_enabled)
	`, cfg.Enabled); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Repository) QQLoginConfig(ctx context.Context) (QQLoginConfig, error) {
	var cfg QQLoginConfig
	err := r.db.QueryRowContext(ctx, `
		select enabled, app_id, app_key, redirect_uri, frontend_base_url
		from bp_auth_qq_login_config
		where id = 1
	`).Scan(&cfg.Enabled, &cfg.AppID, &cfg.AppKey, &cfg.RedirectURI, &cfg.FrontendBaseURL)
	if errors.Is(err, sql.ErrNoRows) {
		return QQLoginConfig{}, nil
	}
	if err != nil {
		return QQLoginConfig{}, err
	}
	cfg.AppKey, err = r.cipher.DecryptCompatible(cfg.AppKey)
	if err != nil {
		return QQLoginConfig{}, err
	}
	return cfg, err
}

func (r *Repository) SaveQQLoginConfig(ctx context.Context, cfg QQLoginConfig) error {
	appKey, err := r.cipher.Encrypt(cfg.AppKey)
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx, `
		insert into bp_auth_qq_login_config(id, enabled, app_id, app_key, redirect_uri, frontend_base_url)
		values (1, ?, ?, ?, ?, ?)
		on duplicate key update
			enabled = values(enabled),
			app_id = values(app_id),
			app_key = values(app_key),
			redirect_uri = values(redirect_uri),
			frontend_base_url = values(frontend_base_url)
	`, cfg.Enabled, cfg.AppID, appKey, cfg.RedirectURI, cfg.FrontendBaseURL)
	return err
}

func requireAffected(result sql.Result) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// SessionConfig 读取登录态有效期配置（bp_auth_session_config 单例行；
// 行缺失时返回迁移默认 30 分钟 / 7 天，不视为错误）。
func (r *Repository) SessionConfig(ctx context.Context) (SessionConfig, error) {
	var cfg SessionConfig
	err := r.db.QueryRowContext(ctx, `
		select access_token_minutes, refresh_token_days
		from bp_auth_session_config
		where id = 1
	`).Scan(&cfg.AccessTokenMinutes, &cfg.RefreshTokenDays)
	if errors.Is(err, sql.ErrNoRows) {
		return SessionConfig{AccessTokenMinutes: 30, RefreshTokenDays: 7}, nil
	}
	if err != nil {
		return SessionConfig{}, err
	}
	return cfg, nil
}

// SaveSessionConfig 保存登录态有效期配置（单行 upsert；区间校验在 service 层）。
func (r *Repository) SaveSessionConfig(ctx context.Context, cfg SessionConfig) error {
	_, err := r.db.ExecContext(ctx, `
		insert into bp_auth_session_config (id, access_token_minutes, refresh_token_days)
		values (1, ?, ?)
		on duplicate key update
		  access_token_minutes = values(access_token_minutes),
		  refresh_token_days = values(refresh_token_days)
	`, cfg.AccessTokenMinutes, cfg.RefreshTokenDays)
	return err
}
