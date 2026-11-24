package systemsettings_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"bugpk-api/internal/config"
	"bugpk-api/internal/domain/auth"
	"bugpk-api/internal/domain/systemsettings"
	apperrors "bugpk-api/internal/pkg/errors"
	"bugpk-api/internal/pkg/secretbox"
	"bugpk-api/internal/pkg/testdb"

	"github.com/cloudwego/hertz/pkg/protocol/consts"
	_ "github.com/go-sql-driver/mysql"
)

func TestProviderSecretsAreEncryptedAtRestAndReadableByAuth(t *testing.T) {
	dsn := os.Getenv("BUGPK_SYSTEM_SETTINGS_INTEGRATION_DSN")
	if dsn == "" {
		t.Skip("BUGPK_SYSTEM_SETTINGS_INTEGRATION_DSN is not configured")
	}
	if err := testdb.ValidateIsolatedMySQLDSN(dsn); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("open integration database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("ping integration database: %v", err)
	}

	goodCipher := secretbox.New("integration-system-config-secret-at-least-32-bytes")
	repo := systemsettings.NewRepository(db, goodCipher)
	if _, err := db.ExecContext(ctx, `
		insert into bp_message_provider_config(channel,provider_key,enabled,config_ref,extra_config)
		values('email','smtp',1,'DB',json_object('authCode','legacy-smtp-secret','senderEmail','ops@example.com','senderName','BugPk-Api','smtpPort',587,'smtpServer','smtp.example.com','smtpTLS',true))
		on duplicate key update enabled=values(enabled),config_ref=values(config_ref),extra_config=values(extra_config)
	`); err != nil {
		t.Fatalf("seed legacy email config: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		insert into bp_message_provider_config(channel,provider_key,enabled,config_ref,extra_config)
		values('sms','aliyun',0,'DB',json_object('accessKeyId','legacy-id','accessKeySecret','legacy-sms-secret'))
		on duplicate key update enabled=values(enabled),config_ref=values(config_ref),extra_config=values(extra_config)
	`); err != nil {
		t.Fatalf("seed legacy mobile config: %v", err)
	}
	if _, err := db.ExecContext(ctx, `update bp_auth_qq_login_config set app_key='legacy-qq-secret' where id=1`); err != nil {
		t.Fatalf("seed legacy QQ config: %v", err)
	}
	if err := repo.MigrateLegacySecrets(ctx); err != nil {
		t.Fatalf("migrate legacy provider secrets: %v", err)
	}
	assertDatabaseDoesNotContain(t, ctx, db, `select cast(extra_config as char) from bp_message_provider_config where channel='email' and provider_key='smtp'`, "legacy-smtp-secret")
	assertDatabaseDoesNotContain(t, ctx, db, `select cast(extra_config as char) from bp_message_provider_config where channel='sms' and provider_key='aliyun'`, "legacy-sms-secret")
	assertDatabaseDoesNotContain(t, ctx, db, `select app_key from bp_auth_qq_login_config where id=1`, "legacy-qq-secret")

	if err := repo.SaveEmailConfig(ctx, systemsettings.EmailConfig{
		AuthCode:    "smtp-plain-secret",
		Enabled:     1,
		SenderEmail: "ops@example.com",
		SenderName:  "BugPk-Api",
		SMTPPort:    587,
		SMTPServer:  "smtp.example.com",
		SMTPTLS:     true,
	}); err != nil {
		t.Fatalf("save encrypted email config: %v", err)
	}
	if err := repo.SaveMobileConfig(ctx, systemsettings.MobileConfig{
		AccessKeyID:     "aliyun-access-key-id",
		AccessKeySecret: "aliyun-plain-secret",
		Enabled:         0,
		Endpoint:        "dypnsapi.aliyuncs.com",
		Provider:        "aliyun",
		Region:          "cn-hangzhou",
	}); err != nil {
		t.Fatalf("save encrypted mobile config: %v", err)
	}
	if err := repo.SaveQQLoginConfig(ctx, systemsettings.QQLoginConfig{
		AppID:           "qq-app-id",
		AppKey:          "qq-plain-secret",
		Enabled:         1,
		FrontendBaseURL: "https://frontend.example.com",
		RedirectURI:     "https://api.example.com/system-api/auth/qq/callback",
	}); err != nil {
		t.Fatalf("save encrypted QQ config: %v", err)
	}

	assertDatabaseDoesNotContain(t, ctx, db, `select cast(extra_config as char) from bp_message_provider_config where channel='email' and provider_key='smtp'`, "smtp-plain-secret")
	assertDatabaseDoesNotContain(t, ctx, db, `select cast(extra_config as char) from bp_message_provider_config where channel='sms' and provider_key='aliyun'`, "aliyun-plain-secret")
	assertDatabaseDoesNotContain(t, ctx, db, `select app_key from bp_auth_qq_login_config where id=1`, "qq-plain-secret")

	emailCfg, err := repo.EmailConfig(ctx)
	if err != nil || emailCfg.AuthCode != "smtp-plain-secret" {
		t.Fatalf("read email config = (%q, %v)", emailCfg.AuthCode, err)
	}
	mobileCfg, err := repo.MobileConfig(ctx)
	if err != nil || mobileCfg.AccessKeySecret != "aliyun-plain-secret" {
		t.Fatalf("read mobile config = (%q, %v)", mobileCfg.AccessKeySecret, err)
	}
	qqCfg, err := repo.QQLoginConfig(ctx)
	if err != nil || qqCfg.AppKey != "qq-plain-secret" {
		t.Fatalf("read QQ config = (%q, %v)", qqCfg.AppKey, err)
	}

	authRepo := auth.NewRepository(db, goodCipher)
	authEmail, ok, err := authRepo.EmailMessageConfig(ctx)
	if err != nil || !ok || authEmail.SMTPPassword != "smtp-plain-secret" {
		t.Fatalf("auth email config = (%q, %v, %v)", authEmail.SMTPPassword, ok, err)
	}

	wrongRepo := auth.NewRepository(db, secretbox.New("different-system-config-secret-at-least-32-bytes"))
	_, _, err = wrongRepo.EmailMessageConfig(ctx)
	if !errors.Is(err, secretbox.ErrDecrypt) {
		t.Fatalf("wrong-key repository error = %v", err)
	}
	verification := auth.NewVerificationCodeService(
		config.MessageConfig{},
		config.AuthConfig{Secret: strings.Repeat("a", 32)},
		wrongRepo,
		nil,
		nil,
	)
	err = verification.SendRegisterEmail(ctx, "user@example.com", "127.0.0.1", "")
	business, isBusiness := apperrors.AsBusinessError(err)
	if !isBusiness || business.StatusCode != consts.StatusServiceUnavailable || !errors.Is(err, secretbox.ErrDecrypt) {
		t.Fatalf("wrong-key send error = %v, want service unavailable", err)
	}
}

func assertDatabaseDoesNotContain(t *testing.T, ctx context.Context, db *sql.DB, query, plain string) {
	t.Helper()
	var stored string
	if err := db.QueryRowContext(ctx, query).Scan(&stored); err != nil {
		t.Fatalf("query encrypted config: %v", err)
	}
	if strings.Contains(stored, plain) || !strings.Contains(stored, "enc:v1:") {
		t.Fatalf("configuration was not encrypted at rest: %q", stored)
	}
}
