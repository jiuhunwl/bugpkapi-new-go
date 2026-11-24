package app

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"bugpk-api/internal/config"
	"bugpk-api/internal/pkg/passwordpolicy"

	"golang.org/x/crypto/bcrypt"
)

const (
	// 历史版本发布过的口令不再以明文保留，仅保留 SHA-256 指纹用于拒绝复用。
	legacyDevelopmentAdminFingerprint = "b3c964bac178bbcd5c4e541cadb3e0f1f4677e4cb63740bc0ef87093cff3ccfb"
	// 仅用于识别并轮换仍携带历史种子哈希的账号。
	legacyInitialAdminHash = "$2a$10$JXmV1cjkkdmh3s/fRfPjKuaoOFtwQrxHmWoJLVoK5SqGcqG/RtBb6"
)

// These were published as bootstrap credentials or copy/paste placeholders.
// They are detection values only, never startup defaults.
var publishedInitialAdminPasswords = [...]string{
	"123456",
	"请设置8-64位强密码",
	"replace-with-a-unique-strong-bootstrap-password",
}

func ensureInitialAdminPassword(ctx context.Context, db *sql.DB, cfg config.Config) (bool, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()

	// Lock the stable administrator role row so concurrent application instances
	// cannot race while bootstrapping a completely empty installation.
	var adminRoleID int64
	if err := tx.QueryRowContext(ctx, `
		select id
		from bp_role
		where role_key='admin' and status=1
		order by id
		limit 1
		for update
	`).Scan(&adminRoleID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, errors.New("active administrator role is required for bootstrap")
		}
		return false, err
	}

	var currentHash string
	if err := tx.QueryRowContext(ctx, `select password_hash from bp_user where id=1 for update`).Scan(&currentHash); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			password, passwordErr := initialAdminPassword(cfg)
			if passwordErr != nil {
				return false, passwordErr
			}
			hash, hashErr := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
			if hashErr != nil {
				return false, hashErr
			}
			if _, insertErr := tx.ExecContext(ctx, `
				insert into bp_user(id,username,password_hash,display_name,home_path,status,token_version)
				values (1,'admin',?,'Administrator','/console/workspace',1,0)
			`, string(hash)); insertErr != nil {
				return false, fmt.Errorf("create initial administrator: %w", insertErr)
			}
			if _, roleErr := tx.ExecContext(ctx, `
				insert into bp_user_role(user_id,role_id)
				values (1,?)
				on duplicate key update role_id=values(role_id)
			`, adminRoleID); roleErr != nil {
				return false, fmt.Errorf("assign initial administrator role: %w", roleErr)
			}
			if commitErr := tx.Commit(); commitErr != nil {
				return false, commitErr
			}
			return true, nil
		}
		return false, err
	}
	if !isPublishedInitialAdminHash(currentHash) {
		if err := tx.Commit(); err != nil {
			return false, err
		}
		return false, nil
	}

	password, err := initialAdminPassword(cfg)
	if err != nil {
		return false, err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return false, err
	}
	result, err := tx.ExecContext(ctx, `
		update bp_user
		set password_hash=?, token_version=token_version+1
		where id=1 and password_hash=?
	`, string(hash), currentHash)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if affected != 1 {
		return false, errors.New("initial administrator password changed concurrently")
	}
	// Only the original seed needs bootstrap role assignment. A previously
	// initialized account may have been deliberately demoted; rotating its
	// published password must not restore administrator privileges.
	if currentHash == legacyInitialAdminHash {
		if _, err := tx.ExecContext(ctx, `
			insert into bp_user_role(user_id,role_id)
			values (1,?)
			on duplicate key update role_id=values(role_id)
		`, adminRoleID); err != nil {
			return false, fmt.Errorf("assign initial administrator role: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func initialAdminPassword(cfg config.Config) (string, error) {
	password := cfg.Auth.InitialAdminPassword
	trimmed := strings.TrimSpace(password)
	if trimmed == "" {
		return "", fmt.Errorf("INITIAL_ADMIN_PASSWORD is required to create or secure the initial administrator in every environment")
	}
	for _, published := range publishedInitialAdminPasswords {
		if trimmed == published {
			return "", fmt.Errorf("INITIAL_ADMIN_PASSWORD must not reuse a publicly documented default password or placeholder")
		}
	}
	if passwordFingerprint(trimmed) == legacyDevelopmentAdminFingerprint {
		return "", fmt.Errorf("INITIAL_ADMIN_PASSWORD must not reuse a publicly documented default password or placeholder")
	}
	if !passwordpolicy.Valid(password) {
		return "", fmt.Errorf("INITIAL_ADMIN_PASSWORD must be 8 to 64 characters and no more than 72 bytes")
	}
	return password, nil
}

func passwordFingerprint(password string) string {
	sum := sha256.Sum256([]byte(password))
	return hex.EncodeToString(sum[:])
}

func isPublishedInitialAdminHash(hash string) bool {
	if hash == legacyInitialAdminHash {
		return true
	}
	// bcrypt uses a random salt; comparing hash strings alone misses accounts
	// already initialized with the old published development password.
	for _, published := range publishedInitialAdminPasswords {
		if bcrypt.CompareHashAndPassword([]byte(hash), []byte(published)) == nil {
			return true
		}
	}
	return false
}
