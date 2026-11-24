package auth

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"bugpk-api/internal/config"
	"bugpk-api/internal/pkg/accountstatus"
	"bugpk-api/internal/pkg/iphash"
	"bugpk-api/internal/pkg/secretbox"

	"github.com/go-sql-driver/mysql"
)

type Repository struct {
	cipher *secretbox.Cipher
	db     *sql.DB
}

var ErrRefreshAuditInactive = errors.New("refresh token audit is inactive")

func NewRepository(db *sql.DB, ciphers ...*secretbox.Cipher) *Repository {
	cipher := secretbox.New("")
	if len(ciphers) > 0 && ciphers[0] != nil {
		cipher = ciphers[0]
	}
	return &Repository{db: db, cipher: cipher}
}

var userSelectColumns = `
		select id, username, password_hash, display_name, email, mobile, ` + accountstatus.EffectiveStatusSQL("") + `,
		       ban_permanent, coalesce(date_format(ban_until, '%Y-%m-%d %H:%i:%s'), ''), coalesce(ban_reason, ''), token_version,
		       totp_enabled, coalesce(totp_secret, '')
	`

func (r *Repository) FindUserByAccount(ctx context.Context, account string) (User, error) {
	row := r.db.QueryRowContext(ctx, userSelectColumns+`
		from bp_user
		where username = ? or email = ?
		limit 1
	`, account, account)
	return r.scanUser(ctx, row)
}

func (r *Repository) FindPasswordResetUserByAccount(ctx context.Context, account string) (User, error) {
	row := r.db.QueryRowContext(ctx, userSelectColumns+`
		from bp_user
		where username = ? or email = ? or mobile = ?
		limit 1
	`, account, account, account)
	return r.scanUser(ctx, row)
}

func (r *Repository) FindUserByID(ctx context.Context, userID int64) (User, error) {
	row := r.db.QueryRowContext(ctx, userSelectColumns+`
		from bp_user
		where id = ?
		limit 1
	`, userID)
	return r.scanUser(ctx, row)
}

func (r *Repository) scanUser(ctx context.Context, scanner interface {
	Scan(dest ...any) error
}) (User, error) {
	var user User
	var email sql.NullString
	var mobile sql.NullString
	var totpEnabled int
	var totpSecret string
	if err := scanner.Scan(
		&user.ID,
		&user.Username,
		&user.PasswordHash,
		&user.DisplayName,
		&email,
		&mobile,
		&user.Status,
		&user.BanPermanent,
		&user.BanUntil,
		&user.BanReason,
		&user.TokenVersion,
		&totpEnabled,
		&totpSecret,
	); err != nil {
		return User{}, err
	}
	if email.Valid {
		user.Email = &email.String
	}
	if mobile.Valid {
		user.Mobile = &mobile.String
	}
	user.TOTPEnabled = totpEnabled == 1
	// totp_secret 是 secretbox 密文（enc:v1:...）。解密失败视为账号数据
	// 异常直接报错，绝不能静默降级为「未启用 2FA」放行登录。
	if totpSecret != "" {
		plain, err := r.cipher.DecryptCompatible(totpSecret)
		if err != nil {
			return User{}, fmt.Errorf("decrypt totp secret: %w", err)
		}
		user.TOTPSecret = plain
	}
	roles, err := r.RoleKeysByUserID(ctx, user.ID)
	if err != nil {
		return User{}, err
	}
	user.Roles = roles
	return user, nil
}

func (r *Repository) RoleKeysByUserID(ctx context.Context, userID int64) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, `
		select r.role_key
		from bp_role r
		inner join bp_user_role ur on ur.role_id = r.id
		where ur.user_id = ? and r.status = 1
		order by r.id
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var roles []string
	for rows.Next() {
		var role string
		if err := rows.Scan(&role); err != nil {
			return nil, err
		}
		roles = append(roles, role)
	}
	return roles, rows.Err()
}

func (r *Repository) RegisterConfig(ctx context.Context) (RegisterConfig, error) {
	row := r.db.QueryRowContext(ctx, `
		select register_enabled,
		       coalesce(default_role_id, 0),
		       email_register_enabled,
		       mobile_register_enabled,
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
		       coalesce(register_risk_shadow_mode, 1),
		       coalesce(register_risk_quarantine_score, 40),
		       coalesce(register_risk_reject_score, 75),
		       oauth_register_enabled,
		       oauth_register_mode_default,
		       oauth_register_ip_hour_limit,
		       oauth_register_ip_day_limit,
		       coalesce(oauth_register_ip_max_accounts, 0),
		       oauth_require_verified_email
		from bp_auth_register_config
		where id = 1
	`)
	var cfg RegisterConfig
	var registerEnabled, emailEnabled, mobileEnabled, oauthEnabled, oauthVerified int
	var riskEnabled, riskShadow int
	if err := row.Scan(
		&registerEnabled,
		&cfg.DefaultRoleID,
		&emailEnabled,
		&mobileEnabled,
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
		&riskEnabled,
		&riskShadow,
		&cfg.RegisterRiskQuarantineScore,
		&cfg.RegisterRiskRejectScore,
		&oauthEnabled,
		&cfg.OAuthRegisterModeDefault,
		&cfg.OAuthRegisterIPHourLimit,
		&cfg.OAuthRegisterIPDayLimit,
		&cfg.OAuthRegisterIPMaxAccounts,
		&oauthVerified,
	); err != nil {
		return RegisterConfig{}, err
	}
	cfg.RegisterEnabled = registerEnabled == 1
	cfg.EmailRegisterEnabled = emailEnabled == 1
	cfg.MobileRegisterEnabled = mobileEnabled == 1
	cfg.OAuthRegisterEnabled = oauthEnabled == 1
	cfg.OAuthRequireVerifiedEmail = oauthVerified == 1
	cfg.RegisterRiskEnabled = riskEnabled == 1
	cfg.RegisterRiskShadowMode = riskShadow == 1
	return cfg, nil
}

func (r *Repository) PublicRegisterRoles(ctx context.Context) ([]RegisterRole, error) {
	rows, err := r.db.QueryContext(ctx, `
		select r.id, r.role_key, r.role_name, r.qps_limit,
		       coalesce(cfg.default_role_id = r.id, 0)
		from bp_role r
		left join bp_auth_register_config cfg on cfg.id = 1
		where r.status = 1
		  and r.self_register_enabled = 1
		  and r.role_key <> 'admin'
		order by coalesce(cfg.default_role_id = r.id, 0) desc,
		         case when r.role_key = 'user' then 0 else 1 end,
		         r.id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	roles := make([]RegisterRole, 0)
	for rows.Next() {
		var role RegisterRole
		var id int64
		if err := rows.Scan(&id, &role.RoleKey, &role.Name, &role.QPSLimit, &role.Default); err != nil {
			return nil, err
		}
		role.ID = strconv.FormatInt(id, 10)
		roles = append(roles, role)
	}
	return roles, rows.Err()
}

func (r *Repository) CaptchaProviderConfig(ctx context.Context, scene, fallback string) (CaptchaProviderConfig, error) {
	rows, err := r.db.QueryContext(ctx, `
		select provider_key, scene, enabled,
		       coalesce(scene_enabled, 1),
		       coalesce(site_key, ''),
		       coalesce(secret_ref, ''),
		       coalesce(timeout_ms, 0),
		       coalesce(failure_policy, ''),
		       coalesce(cast(extra_config as char), '')
		from bp_captcha_provider_config
		where enabled = 1 and scene in (?, 'default')
		order by
			coalesce(explicit_provider, 0) desc,
			case when scene = ? then 0 else 1 end,
			id
		limit 1
	`, scene, scene)
	if err != nil {
		return CaptchaProviderConfig{}, err
	}
	defer rows.Close()

	if rows.Next() {
		var cfg CaptchaProviderConfig
		var providerEnabled, sceneEnabled int
		if err := rows.Scan(&cfg.ProviderKey, &cfg.Scene, &providerEnabled, &sceneEnabled, &cfg.SiteKey, &cfg.SecretRef, &cfg.TimeoutMS, &cfg.FailurePolicy, &cfg.ExtraConfig); err != nil {
			return CaptchaProviderConfig{}, err
		}
		cfg.Enabled = sceneEnabled == 1
		// Inherited from the `default` scene: the provider is shared, but the
		// scene switch is not. Keep the scene's own enable/disable decision.
		if cfg.Scene != scene {
			if err := r.sceneSwitch(ctx, scene, &cfg.Enabled); err != nil {
				return CaptchaProviderConfig{}, err
			}
		}
		if r.cipher != nil {
			cfg.SecretRef, err = r.cipher.DecryptCompatible(cfg.SecretRef)
			if err != nil {
				return CaptchaProviderConfig{}, err
			}
		}
		return cfg, nil
	}
	if err := rows.Err(); err != nil {
		return CaptchaProviderConfig{}, err
	}
	return CaptchaProviderConfig{
		ProviderKey:   fallback,
		Scene:         scene,
		Enabled:       true,
		TimeoutMS:     3000,
		FailurePolicy: "deny",
	}, nil
}

// sceneSwitch applies the scene-level enable flag when the resolved provider
// was inherited from the `default` scene. A scene without any row of its own
// keeps the default scene's flag, as before inheritance existed.
func (r *Repository) sceneSwitch(ctx context.Context, scene string, enabled *bool) error {
	var value int
	err := r.db.QueryRowContext(ctx, `
		select coalesce(scene_enabled, 1)
		from bp_captcha_provider_config
		where scene = ?
		order by id
		limit 1
	`, scene).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	*enabled = value == 1
	return nil
}

func (r *Repository) CaptchaGlobalEnabled(ctx context.Context) (bool, error) {
	var enabled int
	err := r.db.QueryRowContext(ctx, `
		select enabled
		from bp_captcha_global_config
		where id = 1
	`).Scan(&enabled)
	if errors.Is(err, sql.ErrNoRows) {
		// Preserve the previous behavior during a rolling upgrade.
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return enabled == 1, nil
}

func (r *Repository) EmailMessageConfig(ctx context.Context) (config.EmailMessageConfig, bool, error) {
	row := r.db.QueryRowContext(ctx, `
		select enabled, coalesce(config_ref, ''), coalesce(cast(extra_config as char), '{}')
		from bp_message_provider_config
		where channel = 'email' and provider_key = 'smtp'
		limit 1
	`)
	var enabled int
	var configRef string
	var extraText string
	if err := row.Scan(&enabled, &configRef, &extraText); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return config.EmailMessageConfig{}, false, nil
		}
		return config.EmailMessageConfig{}, false, err
	}
	if !strings.EqualFold(strings.TrimSpace(configRef), "DB") {
		return config.EmailMessageConfig{}, false, nil
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
		return config.EmailMessageConfig{}, false, err
	}
	authCode, err := r.cipher.DecryptCompatible(extra.AuthCode)
	if err != nil {
		return config.EmailMessageConfig{}, false, err
	}
	cfg := config.EmailMessageConfig{
		Provider:     "smtp",
		Enabled:      enabled == 1,
		SMTPHost:     strings.TrimSpace(extra.SMTPServer),
		SMTPPort:     extra.SMTPPort,
		SMTPUsername: strings.TrimSpace(extra.SenderEmail),
		SMTPPassword: strings.TrimSpace(authCode),
		SMTPFrom:     strings.TrimSpace(extra.SenderEmail),
		SMTPFromName: strings.TrimSpace(extra.SenderName),
		SMTPTLS:      true,
	}
	if extra.SMTPTLS != nil {
		cfg.SMTPTLS = *extra.SMTPTLS
	}
	return cfg, true, nil
}

func (r *Repository) SiteName(ctx context.Context) (string, error) {
	var siteName string
	err := r.db.QueryRowContext(ctx, `
		select coalesce(site_name, '')
		from bp_site_config
		where id = 1
	`).Scan(&siteName)
	if errors.Is(err, sql.ErrNoRows) {
		return defaultEmailSiteName, nil
	}
	if err != nil {
		return "", err
	}
	return normalizedEmailSiteName(siteName), nil
}

func (r *Repository) CreateLocalUser(ctx context.Context, req RegisterRequest, passwordHash string, giftPoints int64, ipHash []byte) (User, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, err
	}
	defer func() {
		_ = tx.Rollback()
	}()

	exists, err := countExistingUser(ctx, tx, req.Username, req.Email, req.Mobile)
	if err != nil {
		return User{}, err
	}
	if exists > 0 {
		return User{}, ErrUserExists
	}

	// The public request may still contain the legacy roleId field. Never use it
	// for authorization-sensitive role assignment: registration is entirely
	// controlled by the server-side default role configuration.
	roleID, err := registrationRoleID(ctx, tx)
	if err != nil {
		return User{}, err
	}

	result, err := tx.ExecContext(ctx, `
		insert into bp_user(primary_role_id, username, password_hash, display_name, email, mobile, home_path, points, status, last_login_at)
		values (?, ?, ?, ?, ?, ?, '/workspace', ?, 1, now())
	`, roleID, req.Username, passwordHash, req.Username, nullIfBlank(req.Email), nullIfBlank(req.Mobile), giftPoints)
	if err != nil {
		return User{}, err
	}
	userID, err := result.LastInsertId()
	if err != nil {
		return User{}, err
	}

	roleResult, err := tx.ExecContext(ctx, `
		insert into bp_user_role(user_id, role_id)
		values (?, ?)
	`, userID, roleID)
	if err != nil {
		return User{}, err
	}
	rowsAffected, err := roleResult.RowsAffected()
	if err != nil {
		return User{}, err
	}
	if rowsAffected != 1 {
		return User{}, errors.New("default user role missing")
	}

	// Bind the new account to its registration IP inside the same transaction.
	// The primary key on ip_hash makes the 1-account-per-IP cap race-free at
	// the storage layer; a duplicate key means the cap (configured as 1) was
	// hit concurrently and the whole registration must roll back.
	if len(ipHash) > 0 {
		if _, err := tx.ExecContext(ctx, `
			insert into bp_register_ip_used(ip_hash, user_id)
			values (?, ?)
		`, ipHash, userID); err != nil {
			var mysqlErr *mysql.MySQLError
			if errors.As(err, &mysqlErr) && mysqlErr.Number == 1062 {
				return User{}, ErrRegisterIPLimit
			}
			return User{}, err
		}
	}

	if err := tx.Commit(); err != nil {
		return User{}, err
	}
	return r.FindUserByID(ctx, userID)
}

// EmailDomainRules returns the admin-managed registration domain policy. The
// whitelist is exclusive when non-empty; the blacklist always rejects.
func (r *Repository) EmailDomainRules(ctx context.Context) (whitelist, blacklist []string, err error) {
	rows, err := r.db.QueryContext(ctx, `
		select rule_kind, domain
		from bp_email_domain_rule
		order by domain
	`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var kind, domain string
		if err := rows.Scan(&kind, &domain); err != nil {
			return nil, nil, err
		}
		switch kind {
		case "whitelist":
			whitelist = append(whitelist, domain)
		case "blacklist":
			blacklist = append(blacklist, domain)
		}
	}
	return whitelist, blacklist, rows.Err()
}

// CountRegisterIPAccounts reports how many accounts were already created from
// the given client IP. The digest matches the value written by CreateLocalUser.
func (r *Repository) CountRegisterIPAccounts(ctx context.Context, ip string) (int64, error) {
	var count int64
	err := r.db.QueryRowContext(ctx, `
		select count(*)
		from bp_register_ip_used
		where ip_hash = ?
	`, iphash.RiskDigest(ip)).Scan(&count)
	return count, err
}

func registrationRoleID(ctx context.Context, tx *sql.Tx) (int64, error) {
	var roleID int64
	if err := tx.QueryRowContext(ctx, `
		select coalesce(
			(select default_role_id from bp_auth_register_config where id = 1),
			(select id from bp_role where role_key = 'user' limit 1)
		)
	`).Scan(&roleID); err != nil {
		return 0, err
	}
	var exists int
	if err := tx.QueryRowContext(ctx, `
		select exists(
			select 1
			from bp_role
			where id = ?
			  and status = 1
			  and self_register_enabled = 1
			  and role_key <> 'admin'
		)
	`, roleID).Scan(&exists); err != nil {
		return 0, err
	}
	if exists != 1 {
		return 0, ErrRegistrationRoleInvalid
	}
	return roleID, nil
}

func validateRegistrationRoleID(ctx context.Context, tx *sql.Tx, roleID int64) error {
	var exists int
	if err := tx.QueryRowContext(ctx, `
		select exists(
			select 1 from bp_role
			where id = ? and status = 1 and self_register_enabled = 1 and role_key <> 'admin'
		)
	`, roleID).Scan(&exists); err != nil {
		return err
	}
	if exists != 1 {
		return ErrRegistrationRoleInvalid
	}
	return nil
}

func (r *Repository) RegistrationRoleValid(ctx context.Context, roleID int64) (bool, error) {
	var exists int
	if err := r.db.QueryRowContext(ctx, `
		select exists(
			select 1 from bp_role
			where id = ? and status = 1 and self_register_enabled = 1 and role_key <> 'admin'
		)
	`, roleID).Scan(&exists); err != nil {
		return false, err
	}
	return exists == 1, nil
}

func consumeRegisterIPQuota(ctx context.Context, tx *sql.Tx, ipHash []byte, channel string, maxAccounts int) error {
	if maxAccounts <= 0 || len(ipHash) == 0 {
		return nil
	}
	channel = strings.TrimSpace(channel)
	if channel == "" {
		channel = "local"
	}
	if _, err := tx.ExecContext(ctx, `
		insert into bp_register_ip_usage(ip_hash, channel, used)
		values(?, ?, 1)
		on duplicate key update used = used + 1
	`, ipHash, channel); err != nil {
		return err
	}
	var used int
	if err := tx.QueryRowContext(ctx, `
		select used from bp_register_ip_usage
		where ip_hash = ? and channel = ?
		for update
	`, ipHash, channel).Scan(&used); err != nil {
		return err
	}
	if used > maxAccounts {
		return ErrRegisterIPLimit
	}
	return nil
}

func countExistingUser(ctx context.Context, tx *sql.Tx, username, email, mobile string) (int64, error) {
	row := tx.QueryRowContext(ctx, `
		select count(*)
		from bp_user
		where username = ?
		   or (? <> '' and email = ?)
		   or (? <> '' and mobile = ?)
	`, username, email, email, mobile, mobile)
	var count int64
	if err := row.Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

func (r *Repository) UpdateLastLogin(ctx context.Context, userID int64) error {
	_, err := r.db.ExecContext(ctx, "update bp_user set last_login_at = now() where id = ?", userID)
	return err
}

func (r *Repository) CreateRefreshAudit(ctx context.Context, user User, jti, device string, issuedAt, expiresAt time.Time) error {
	_, err := r.db.ExecContext(ctx, `
		insert into bp_auth_refresh_token_audit(user_id, jti, device_fingerprint, token_version, issued_at, expires_at)
		values (?, ?, ?, ?, ?, ?)
	`, user.ID, jti, nullIfBlank(device), user.TokenVersion, issuedAt, expiresAt)
	return err
}

func (r *Repository) RotateRefreshAudit(
	ctx context.Context,
	oldInfo RefreshInfo,
	user User,
	newJTI, newDevice string,
	issuedAt, expiresAt time.Time,
) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	result, err := tx.ExecContext(ctx, `
		update bp_auth_refresh_token_audit
		set revoked_at = now(), revoke_reason = 'rotated', last_used_at = now()
		where jti = ? and user_id = ? and token_version = ? and device_fingerprint = ?
		  and revoked_at is null and last_used_at is null
	`, oldInfo.JTI, oldInfo.UserID, oldInfo.TokenVersion, oldInfo.Device)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return ErrRefreshAuditInactive
	}
	if _, err := tx.ExecContext(ctx, `
		insert into bp_auth_refresh_token_audit(user_id, jti, device_fingerprint, token_version, issued_at, expires_at)
		values (?, ?, ?, ?, ?, ?)
	`, user.ID, newJTI, nullIfBlank(newDevice), user.TokenVersion, issuedAt, expiresAt); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Repository) RevokeRefreshAudit(ctx context.Context, jti, reason string) error {
	result, err := r.db.ExecContext(ctx, `
		update bp_auth_refresh_token_audit
		set revoked_at = coalesce(revoked_at, now()), revoke_reason = coalesce(revoke_reason, ?)
		where jti = ?
	`, reason, jti)
	if err != nil {
		return err
	}
	return r.requireRefreshAuditRow(ctx, jti, result)
}

func (r *Repository) requireRefreshAuditRow(ctx context.Context, jti string, result sql.Result) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected > 0 {
		return nil
	}
	var exists int
	if err := r.db.QueryRowContext(ctx, "select exists(select 1 from bp_auth_refresh_token_audit where jti = ?)", jti).Scan(&exists); err != nil {
		return err
	}
	if exists != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func (r *Repository) CreateEmailCode(ctx context.Context, scene, email, codeHash string, expiresAt time.Time) error {
	_, err := r.db.ExecContext(ctx, `
		insert into bp_auth_email_code(scene, email, code_hash, expires_at)
		values (?, ?, ?, ?)
	`, scene, email, codeHash, expiresAt)
	return err
}

// HasFreshEmailCode reports whether an unconsumed, unexpired code created at or
// after `since` already exists for this scene + email. It backs the send
// cooldown: callers reuse the in-flight code instead of emitting a new email.
// Uses idx_bp_auth_email_code_scene_email (scene, email, expires_at).
func (r *Repository) HasFreshEmailCode(ctx context.Context, scene, email string, since time.Time) (bool, error) {
	var exists int
	err := r.db.QueryRowContext(ctx, `
		select 1
		from bp_auth_email_code
		where scene = ?
		  and email = ?
		  and consumed_at is null
		  and expires_at > now()
		  and created_at >= ?
		limit 1
	`, scene, email, since).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (r *Repository) RevokeEmailCode(ctx context.Context, scene, email, codeHash string) error {
	result, err := r.db.ExecContext(ctx, `
		update bp_auth_email_code
		set consumed_at = now()
		where scene = ? and email = ? and code_hash = ? and consumed_at is null
	`, scene, email, codeHash)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return errors.New("email verification code revoke affected unexpected rows")
	}
	return nil
}

func (r *Repository) ConsumeEmailCode(ctx context.Context, scene, email, codeHash string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		_ = tx.Rollback()
	}()

	var id int64
	var storedHash string
	err = tx.QueryRowContext(ctx, `
		select id, code_hash
		from bp_auth_email_code
		where scene = ?
		  and email = ?
		  and consumed_at is null
		  and expires_at > now()
		  and attempts < 5
		order by id desc
		limit 1
		for update
	`, scene, email).Scan(&id, &storedHash)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrVerificationCodeInvalid
		}
		return err
	}
	if subtle.ConstantTimeCompare([]byte(storedHash), []byte(codeHash)) != 1 {
		// 6 位码在有效期内不能无限试。第 5 次失败直接把该码作废，
		// 需要重新发送验证码；成功路径不增加计数。
		if _, err := tx.ExecContext(ctx, `
			update bp_auth_email_code
			set attempts = attempts + 1,
			    consumed_at = if(attempts + 1 >= 5, now(), consumed_at)
			where id = ?
		`, id); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		return ErrVerificationCodeInvalid
	}
	result, err := tx.ExecContext(ctx, `
		update bp_auth_email_code
		set consumed_at = now()
		where scene = ? and email = ? and consumed_at is null and expires_at > now()
	`, scene, email)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected < 1 {
		return ErrVerificationCodeInvalid
	}
	return tx.Commit()
}

func (r *Repository) CreateMobileCode(ctx context.Context, scene, mobile, codeHash string, expiresAt time.Time) error {
	_, err := r.db.ExecContext(ctx, `
		insert into bp_auth_mobile_code(scene, mobile, code_hash, expires_at)
		values (?, ?, ?, ?)
	`, scene, mobile, codeHash, expiresAt)
	return err
}

func (r *Repository) ConsumeMobileCode(ctx context.Context, scene, mobile, codeHash string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		_ = tx.Rollback()
	}()

	var id int64
	err = tx.QueryRowContext(ctx, `
		select id
		from bp_auth_mobile_code
		where scene = ?
		  and mobile = ?
		  and code_hash = ?
		  and consumed_at is null
		  and expires_at > now()
		order by id desc
		limit 1
		for update
	`, scene, mobile, codeHash).Scan(&id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrVerificationCodeInvalid
		}
		return err
	}
	if _, err := tx.ExecContext(ctx, "update bp_auth_mobile_code set consumed_at = now() where id = ?", id); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Repository) UpdatePasswordHashAndBumpVersion(ctx context.Context, userID int64, passwordHash string) error {
	result, err := r.db.ExecContext(ctx, `
		update bp_user
		set password_hash = ?, token_version = token_version + 1
		where id = ? and `+accountstatus.EffectiveStatusSQL("")+` = 1
	`, passwordHash, userID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return ErrUserUnavailable
	}
	return nil
}

func nullIfBlank(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return strings.TrimSpace(value)
}

var ErrUserExists = fmt.Errorf("user already exists")
var ErrUserUnavailable = fmt.Errorf("user unavailable")
var ErrVerificationCodeInvalid = fmt.Errorf("verification code invalid")
var ErrRegistrationRoleInvalid = fmt.Errorf("registration role is invalid")
var ErrRegisterIPLimit = fmt.Errorf("registration ip account cap reached")

// EnableTOTP 在同一事务里写入密钥、启用位、确认时间，并替换该用户的
// 全部恢复码。任何一步失败整体回滚，不会出现「启用成功但恢复码缺失」。
func (r *Repository) EnableTOTP(ctx context.Context, userID int64, encryptedSecret string, recoveryHashes []string, confirmedAt time.Time) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `
		update bp_user
		set totp_secret = ?, totp_enabled = 1, totp_confirmed_at = ?
		where id = ?
	`, encryptedSecret, confirmedAt, userID)
	if err != nil {
		return err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return err
	} else if affected != 1 {
		return ErrUserUnavailable
	}
	if _, err := tx.ExecContext(ctx, `delete from bp_user_totp_recovery_code where user_id = ?`, userID); err != nil {
		return err
	}
	for _, hash := range recoveryHashes {
		if _, err := tx.ExecContext(ctx, `
			insert into bp_user_totp_recovery_code(user_id, code_hash) values (?, ?)
		`, userID, hash); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// DisableTOTP 清空 TOTP 绑定并删除全部恢复码。
func (r *Repository) DisableTOTP(ctx context.Context, userID int64) error {
	result, err := r.db.ExecContext(ctx, `
		update bp_user
		set totp_secret = null, totp_enabled = 0, totp_confirmed_at = null
		where id = ?
	`, userID)
	if err != nil {
		return err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return err
	} else if affected != 1 {
		return ErrUserUnavailable
	}
	_, err = r.db.ExecContext(ctx, `delete from bp_user_totp_recovery_code where user_id = ?`, userID)
	return err
}

// ConsumeRecoveryCode 原子抢占一条未使用的恢复码；影响行数 0 表示
// 码不存在或已被消费，用于防并发重放。
func (r *Repository) ConsumeRecoveryCode(ctx context.Context, userID int64, codeHash string) (bool, error) {
	result, err := r.db.ExecContext(ctx, `
		update bp_user_totp_recovery_code
		set used_at = now()
		where user_id = ? and code_hash = ? and used_at is null
	`, userID, codeHash)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected == 1, nil
}

// CountRecoveryCodes 统计剩余可用恢复码，用于后台状态展示。
func (r *Repository) CountRecoveryCodes(ctx context.Context, userID int64) (int, error) {
	var count int
	err := r.db.QueryRowContext(ctx, `
		select count(*)
		from bp_user_totp_recovery_code
		where user_id = ? and used_at is null
	`, userID).Scan(&count)
	return count, err
}
