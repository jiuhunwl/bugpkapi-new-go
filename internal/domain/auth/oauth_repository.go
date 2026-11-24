package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"bugpk-api/internal/pkg/secretbox"

	"github.com/go-sql-driver/mysql"
)

type OAuthRepository struct {
	db     *sql.DB
	cipher *secretbox.Cipher
}

func NewOAuthRepository(db *sql.DB, ciphers ...*secretbox.Cipher) *OAuthRepository {
	cipher := secretbox.New("")
	if len(ciphers) > 0 && ciphers[0] != nil {
		cipher = ciphers[0]
	}
	return &OAuthRepository{db: db, cipher: cipher}
}

var (
	ErrOAuthIdentityExists = errors.New("oauth identity already exists")
	ErrOAuthAlreadyBound   = errors.New("oauth provider already bound to this user")
	ErrOAuthUsernameTaken  = errors.New("oauth username is taken")
	ErrOAuthEmailConflict  = errors.New("oauth email is already registered")
)

func (r *OAuthRepository) GlobalConfig(ctx context.Context) (OAuthGlobalConfig, error) {
	var cfg OAuthGlobalConfig
	err := r.db.QueryRowContext(ctx, `
		select coalesce(frontend_base_url, '')
		from bp_auth_oauth_config
		where id = 1
	`).Scan(&cfg.FrontendBaseURL)
	if errors.Is(err, sql.ErrNoRows) {
		return OAuthGlobalConfig{}, nil
	}
	return cfg, err
}

func (r *OAuthRepository) SaveGlobalConfig(ctx context.Context, cfg OAuthGlobalConfig) error {
	_, err := r.db.ExecContext(ctx, `
		insert into bp_auth_oauth_config(id, frontend_base_url)
		values(1, ?)
		on duplicate key update frontend_base_url = values(frontend_base_url)
	`, strings.TrimSpace(cfg.FrontendBaseURL))
	return err
}

func (r *OAuthRepository) Providers(ctx context.Context, onlyEnabled bool) ([]OAuthProviderConfig, error) {
	query := `
		select id, provider_key, display_name, icon_key, driver, provider_group, enabled,
		       register_mode, default_role_id, register_gift_points, register_ip_hour_limit,
		       register_ip_day_limit, register_ip_max_accounts, link_by_email,
		       require_verified_email, require_verified_email_mode, min_account_age_days,
		       min_trust_level, require_active, reject_silenced, client_id, client_secret,
		       issuer, authorization_endpoint, token_endpoint, userinfo_endpoint, jwks_uri,
		       scopes, redirect_uri, coalesce(cast(extra_config as char), ''), sort_no
		from bp_auth_oauth_provider
	`
	if onlyEnabled {
		query += ` where enabled = 1`
	}
	query += ` order by sort_no, id`
	rows, err := r.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	providers := make([]OAuthProviderConfig, 0)
	for rows.Next() {
		cfg, err := r.scanProvider(rows)
		if err != nil {
			return nil, err
		}
		providers = append(providers, cfg)
	}
	return providers, rows.Err()
}

func (r *OAuthRepository) ProviderByKey(ctx context.Context, providerKey string) (OAuthProviderConfig, error) {
	row := r.db.QueryRowContext(ctx, `
		select id, provider_key, display_name, icon_key, driver, provider_group, enabled,
		       register_mode, default_role_id, register_gift_points, register_ip_hour_limit,
		       register_ip_day_limit, register_ip_max_accounts, link_by_email,
		       require_verified_email, require_verified_email_mode, min_account_age_days,
		       min_trust_level, require_active, reject_silenced, client_id, client_secret,
		       issuer, authorization_endpoint, token_endpoint, userinfo_endpoint, jwks_uri,
		       scopes, redirect_uri, coalesce(cast(extra_config as char), ''), sort_no
		from bp_auth_oauth_provider
		where provider_key = ?
		limit 1
	`, providerKey)
	return r.scanProvider(row)
}

type oauthProviderScanner interface {
	Scan(dest ...any) error
}

func (r *OAuthRepository) scanProvider(scanner oauthProviderScanner) (OAuthProviderConfig, error) {
	var cfg OAuthProviderConfig
	var enabled, linkByEmail, requireVerified, requireActive, rejectSilenced int
	if err := scanner.Scan(
		&cfg.ID,
		&cfg.ProviderKey,
		&cfg.DisplayName,
		&cfg.IconKey,
		&cfg.Driver,
		&cfg.ProviderGroup,
		&enabled,
		&cfg.RegisterMode,
		&cfg.DefaultRoleID,
		&cfg.RegisterGiftPoints,
		&cfg.RegisterIPHourLimit,
		&cfg.RegisterIPDayLimit,
		&cfg.RegisterIPMaxAccounts,
		&linkByEmail,
		&requireVerified,
		&cfg.RequireVerifiedEmailMode,
		&cfg.MinAccountAgeDays,
		&cfg.MinTrustLevel,
		&requireActive,
		&rejectSilenced,
		&cfg.ClientID,
		&cfg.ClientSecret,
		&cfg.Issuer,
		&cfg.AuthorizationEndpoint,
		&cfg.TokenEndpoint,
		&cfg.UserinfoEndpoint,
		&cfg.JWKSURI,
		&cfg.Scopes,
		&cfg.RedirectURI,
		&cfg.ExtraConfig,
		&cfg.SortNo,
	); err != nil {
		return OAuthProviderConfig{}, err
	}
	cfg.Enabled = enabled == 1
	cfg.LinkByEmail = linkByEmail == 1
	cfg.RequireVerifiedEmail = requireVerified == 1
	cfg.RequireActive = requireActive == 1
	cfg.RejectSilenced = rejectSilenced == 1
	secret, err := r.cipher.DecryptCompatible(cfg.ClientSecret)
	if err != nil {
		return OAuthProviderConfig{}, err
	}
	cfg.ClientSecret = secret
	return cfg, nil
}

func (r *OAuthRepository) CreateProvider(ctx context.Context, cfg OAuthProviderConfig) error {
	secret, err := r.cipher.Encrypt(cfg.ClientSecret)
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx, `
		insert into bp_auth_oauth_provider(
			provider_key, display_name, icon_key, driver, provider_group, enabled, register_mode,
			default_role_id, register_gift_points, register_ip_hour_limit, register_ip_day_limit,
			register_ip_max_accounts, link_by_email, require_verified_email,
			require_verified_email_mode, min_account_age_days, min_trust_level,
			require_active, reject_silenced, client_id, client_secret, issuer,
			authorization_endpoint, token_endpoint, userinfo_endpoint, jwks_uri, scopes,
			redirect_uri, extra_config, sort_no
		) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
	`,
		cfg.ProviderKey, cfg.DisplayName, cfg.IconKey, cfg.Driver, cfg.ProviderGroup,
		boolInt(cfg.Enabled), cfg.RegisterMode, cfg.DefaultRoleID, cfg.RegisterGiftPoints,
		cfg.RegisterIPHourLimit, cfg.RegisterIPDayLimit, cfg.RegisterIPMaxAccounts,
		boolInt(cfg.LinkByEmail), boolInt(cfg.RequireVerifiedEmail), cfg.RequireVerifiedEmailMode,
		cfg.MinAccountAgeDays, cfg.MinTrustLevel, boolInt(cfg.RequireActive), boolInt(cfg.RejectSilenced),
		cfg.ClientID, secret, cfg.Issuer, cfg.AuthorizationEndpoint, cfg.TokenEndpoint,
		cfg.UserinfoEndpoint, cfg.JWKSURI, cfg.Scopes, cfg.RedirectURI, nullIfBlank(cfg.ExtraConfig), cfg.SortNo,
	)
	return err
}

func (r *OAuthRepository) UpdateProvider(ctx context.Context, cfg OAuthProviderConfig) error {
	secret, err := r.cipher.Encrypt(cfg.ClientSecret)
	if err != nil {
		return err
	}
	result, err := r.db.ExecContext(ctx, `
		update bp_auth_oauth_provider
		set display_name = ?, icon_key = ?, driver = ?, provider_group = ?, enabled = ?,
		    register_mode = ?, default_role_id = ?, register_gift_points = ?,
		    register_ip_hour_limit = ?, register_ip_day_limit = ?, register_ip_max_accounts = ?,
		    link_by_email = ?, require_verified_email = ?, require_verified_email_mode = ?,
		    min_account_age_days = ?, min_trust_level = ?, require_active = ?, reject_silenced = ?,
		    client_id = ?, client_secret = ?, issuer = ?, authorization_endpoint = ?,
		    token_endpoint = ?, userinfo_endpoint = ?, jwks_uri = ?, scopes = ?,
		    redirect_uri = ?, extra_config = ?, sort_no = ?
		where provider_key = ?
	`,
		cfg.DisplayName, cfg.IconKey, cfg.Driver, cfg.ProviderGroup, boolInt(cfg.Enabled),
		cfg.RegisterMode, cfg.DefaultRoleID, cfg.RegisterGiftPoints, cfg.RegisterIPHourLimit,
		cfg.RegisterIPDayLimit, cfg.RegisterIPMaxAccounts, boolInt(cfg.LinkByEmail),
		boolInt(cfg.RequireVerifiedEmail), cfg.RequireVerifiedEmailMode, cfg.MinAccountAgeDays,
		cfg.MinTrustLevel, boolInt(cfg.RequireActive), boolInt(cfg.RejectSilenced), cfg.ClientID,
		secret, cfg.Issuer, cfg.AuthorizationEndpoint, cfg.TokenEndpoint, cfg.UserinfoEndpoint,
		cfg.JWKSURI, cfg.Scopes, cfg.RedirectURI, nullIfBlank(cfg.ExtraConfig), cfg.SortNo,
		cfg.ProviderKey,
	)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		// MySQL reports zero for an identical update; confirm the row exists.
		var exists int
		if err := r.db.QueryRowContext(ctx, `select exists(select 1 from bp_auth_oauth_provider where provider_key = ?)`, cfg.ProviderKey).Scan(&exists); err != nil {
			return err
		}
		if exists != 1 {
			return sql.ErrNoRows
		}
	}
	return nil
}

func (r *OAuthRepository) ProviderHasIdentities(ctx context.Context, providerKey string) (bool, error) {
	var count int64
	if err := r.db.QueryRowContext(ctx, `select count(*) from bp_auth_oauth_identity where provider_key = ?`, providerKey).Scan(&count); err != nil {
		return false, err
	}
	return count > 0, nil
}

func (r *OAuthRepository) IdentityBySubject(ctx context.Context, providerKey, subject string) (OAuthIdentity, error) {
	row := r.db.QueryRowContext(ctx, `
		select id, user_id, provider_key, provider_group, subject, union_id, nickname,
		       avatar, email, email_verified,
		       coalesce(date_format(bound_at, '%Y-%m-%d %H:%i:%s'), ''),
		       coalesce(date_format(last_login_at, '%Y-%m-%d %H:%i:%s'), '')
		from bp_auth_oauth_identity
		where provider_key = ? and subject = ?
		limit 1
	`, providerKey, subject)
	return scanOAuthIdentity(row)
}

func (r *OAuthRepository) IdentityByUnion(ctx context.Context, providerGroup, unionID string) (OAuthIdentity, error) {
	if strings.TrimSpace(providerGroup) == "" || strings.TrimSpace(unionID) == "" {
		return OAuthIdentity{}, sql.ErrNoRows
	}
	row := r.db.QueryRowContext(ctx, `
		select id, user_id, provider_key, provider_group, subject, union_id, nickname,
		       avatar, email, email_verified,
		       coalesce(date_format(bound_at, '%Y-%m-%d %H:%i:%s'), ''),
		       coalesce(date_format(last_login_at, '%Y-%m-%d %H:%i:%s'), '')
		from bp_auth_oauth_identity
		where provider_group = ? and union_id = ?
		order by id
		limit 1
	`, providerGroup, unionID)
	return scanOAuthIdentity(row)
}

func (r *OAuthRepository) IdentityByUserProvider(ctx context.Context, userID int64, providerKey string) (OAuthIdentity, error) {
	row := r.db.QueryRowContext(ctx, `
		select id, user_id, provider_key, provider_group, subject, union_id, nickname,
		       avatar, email, email_verified,
		       coalesce(date_format(bound_at, '%Y-%m-%d %H:%i:%s'), ''),
		       coalesce(date_format(last_login_at, '%Y-%m-%d %H:%i:%s'), '')
		from bp_auth_oauth_identity
		where user_id = ? and provider_key = ?
		limit 1
	`, userID, providerKey)
	return scanOAuthIdentity(row)
}

func (r *OAuthRepository) IdentitiesByUser(ctx context.Context, userID int64) ([]OAuthIdentity, error) {
	rows, err := r.db.QueryContext(ctx, `
		select id, user_id, provider_key, provider_group, subject, union_id, nickname,
		       avatar, email, email_verified,
		       coalesce(date_format(bound_at, '%Y-%m-%d %H:%i:%s'), ''),
		       coalesce(date_format(last_login_at, '%Y-%m-%d %H:%i:%s'), '')
		from bp_auth_oauth_identity
		where user_id = ?
		order by id
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	identities := make([]OAuthIdentity, 0)
	for rows.Next() {
		identity, err := scanOAuthIdentity(rows)
		if err != nil {
			return nil, err
		}
		identities = append(identities, identity)
	}
	return identities, rows.Err()
}

func scanOAuthIdentity(scanner oauthProviderScanner) (OAuthIdentity, error) {
	var identity OAuthIdentity
	var emailVerified int
	if err := scanner.Scan(
		&identity.ID,
		&identity.UserID,
		&identity.ProviderKey,
		&identity.ProviderGroup,
		&identity.Subject,
		&identity.UnionID,
		&identity.Nickname,
		&identity.Avatar,
		&identity.Email,
		&emailVerified,
		&identity.BoundAt,
		&identity.LastLoginAt,
	); err != nil {
		return OAuthIdentity{}, err
	}
	identity.EmailVerified = emailVerified == 1
	return identity, nil
}

func (r *OAuthRepository) BindIdentity(ctx context.Context, identity OAuthIdentity) (int64, error) {
	result, err := r.db.ExecContext(ctx, `
		insert into bp_auth_oauth_identity(
			user_id, provider_key, provider_group, subject, union_id, nickname, avatar,
			email, email_verified, last_login_at
		) values(?,?,?,?,?,?,?,?,?, now())
	`,
		identity.UserID, identity.ProviderKey, identity.ProviderGroup, identity.Subject,
		identity.UnionID, identity.Nickname, identity.Avatar, identity.Email,
		boolInt(identity.EmailVerified),
	)
	if err != nil {
		return 0, mapOAuthIdentityInsertError(err)
	}
	return result.LastInsertId()
}

func (r *OAuthRepository) UpdateIdentityProfile(ctx context.Context, identity OAuthIdentity) error {
	result, err := r.db.ExecContext(ctx, `
		update bp_auth_oauth_identity
		set nickname = ?, avatar = ?, email = ?, email_verified = ?, last_login_at = now()
		where id = ?
	`, identity.Nickname, identity.Avatar, identity.Email, boolInt(identity.EmailVerified), identity.ID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (r *OAuthRepository) UnbindIdentity(ctx context.Context, userID int64, providerKey string) (bool, error) {
	result, err := r.db.ExecContext(ctx, `
		delete from bp_auth_oauth_identity where user_id = ? and provider_key = ?
	`, userID, providerKey)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected > 0, nil
}

func (r *OAuthRepository) CountIdentitiesByUser(ctx context.Context, userID int64) (int64, error) {
	var count int64
	err := r.db.QueryRowContext(ctx, `select count(*) from bp_auth_oauth_identity where user_id = ?`, userID).Scan(&count)
	return count, err
}

func (r *OAuthRepository) UserIDByEmail(ctx context.Context, email string) (int64, bool, error) {
	email = strings.TrimSpace(email)
	if email == "" {
		return 0, false, nil
	}
	var userID int64
	err := r.db.QueryRowContext(ctx, `select id from bp_user where email = ? limit 1`, email).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return userID, true, nil
}

func (r *OAuthRepository) UsernameExists(ctx context.Context, username string) (bool, error) {
	var exists int
	if err := r.db.QueryRowContext(ctx, `select exists(select 1 from bp_user where username = ?)`, username).Scan(&exists); err != nil {
		return false, err
	}
	return exists == 1, nil
}

func (r *OAuthRepository) PasswordSet(ctx context.Context, userID int64) (bool, error) {
	var value int
	if err := r.db.QueryRowContext(ctx, `select coalesce(password_set, 1) from bp_user where id = ?`, userID).Scan(&value); err != nil {
		return false, err
	}
	return value == 1, nil
}

type OAuthExternalUser struct {
	Username      string
	DisplayName   string
	Avatar        string
	Email         string
	EmailVerified bool
	PasswordHash  string
	PasswordSet   bool
	GiftPoints    int64
	RoleID        int64
	IPHash        []byte
	IPChannel     string
	IPMaxAccounts int
	Identity      OAuthIdentity
}

func (r *OAuthRepository) CreateExternalUser(ctx context.Context, user OAuthExternalUser) (int64, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	var exists int
	if err := tx.QueryRowContext(ctx, `select exists(select 1 from bp_user where username = ?)`, user.Username).Scan(&exists); err != nil {
		return 0, err
	}
	if exists == 1 {
		return 0, ErrOAuthUsernameTaken
	}
	if strings.TrimSpace(user.Email) != "" {
		if err := tx.QueryRowContext(ctx, `select exists(select 1 from bp_user where email = ?)`, strings.TrimSpace(user.Email)).Scan(&exists); err != nil {
			return 0, err
		}
		if exists == 1 {
			return 0, ErrOAuthEmailConflict
		}
	}
	roleID := user.RoleID
	if roleID <= 0 {
		roleID, err = registrationRoleID(ctx, tx)
		if err != nil {
			return 0, err
		}
	} else if err := validateRegistrationRoleID(ctx, tx, roleID); err != nil {
		return 0, err
	}
	if len(user.IPHash) > 0 && user.IPMaxAccounts > 0 {
		if err := consumeRegisterIPQuota(ctx, tx, user.IPHash, user.IPChannel, user.IPMaxAccounts); err != nil {
			return 0, err
		}
	}
	result, err := tx.ExecContext(ctx, `
		insert into bp_user(
			primary_role_id, username, password_hash, password_set, display_name, avatar,
			email, email_verified, home_path, points, status, last_login_at
		) values(?,?,?,?,?,?,?,?, '/workspace', ?, 1, now())
	`,
		roleID, user.Username, user.PasswordHash, boolInt(user.PasswordSet),
		user.DisplayName, nullIfBlank(user.Avatar), nullIfBlank(user.Email),
		boolInt(user.EmailVerified), user.GiftPoints,
	)
	if err != nil {
		return 0, mapOAuthUserInsertError(err)
	}
	userID, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `insert into bp_user_role(user_id, role_id) values(?,?)`, userID, roleID); err != nil {
		return 0, err
	}
	identity := user.Identity
	identity.UserID = userID
	if _, err := tx.ExecContext(ctx, `
		insert into bp_auth_oauth_identity(
			user_id, provider_key, provider_group, subject, union_id, nickname, avatar,
			email, email_verified, last_login_at
		) values(?,?,?,?,?,?,?,?,?, now())
	`,
		userID, identity.ProviderKey, identity.ProviderGroup, identity.Subject,
		identity.UnionID, identity.Nickname, identity.Avatar, identity.Email,
		boolInt(identity.EmailVerified),
	); err != nil {
		return 0, mapOAuthIdentityInsertError(err)
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return userID, nil
}

func mapOAuthIdentityInsertError(err error) error {
	var mysqlErr *mysql.MySQLError
	if errors.As(err, &mysqlErr) && mysqlErr.Number == 1062 {
		message := strings.ToLower(mysqlErr.Message)
		switch {
		case strings.Contains(message, "uk_bp_auth_oauth_identity_subject"):
			return ErrOAuthIdentityExists
		case strings.Contains(message, "uk_bp_auth_oauth_identity_user_provider"):
			return ErrOAuthAlreadyBound
		}
		return ErrOAuthIdentityExists
	}
	return err
}

func mapOAuthUserInsertError(err error) error {
	var mysqlErr *mysql.MySQLError
	if errors.As(err, &mysqlErr) && mysqlErr.Number == 1062 {
		message := strings.ToLower(mysqlErr.Message)
		switch {
		case strings.Contains(message, "uk_bp_user_username"):
			return ErrOAuthUsernameTaken
		case strings.Contains(message, "uk_bp_user_email"):
			return ErrOAuthEmailConflict
		}
	}
	return err
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func oauthIdentityExists(err error) bool {
	return errors.Is(err, ErrOAuthIdentityExists)
}

func oauthIdentityString(identity OAuthIdentity) string {
	return fmt.Sprintf("%s:%s", identity.ProviderKey, identity.Subject)
}
