package smtpmail

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestValidateConfigRejectsRemotePlaintextAuthentication(t *testing.T) {
	cfg := validTestConfig()
	cfg.TLS = false
	if _, _, _, err := validateConfig(cfg, "user@example.com"); err == nil || !strings.Contains(err.Error(), "requires TLS") {
		t.Fatalf("validateConfig() error = %v", err)
	}
}

func TestValidateConfigAllowsLoopbackPlaintextAuthentication(t *testing.T) {
	cfg := validTestConfig()
	cfg.Host = "127.0.0.1"
	cfg.TLS = false
	if _, _, _, err := validateConfig(cfg, "user@example.com"); err != nil {
		t.Fatalf("loopback development SMTP rejected: %v", err)
	}
}

func TestValidateConfigRejectsIncompleteCredentialsAndPort465WithoutTLS(t *testing.T) {
	cfg := validTestConfig()
	cfg.Password = ""
	if _, _, _, err := validateConfig(cfg, "user@example.com"); err == nil || !strings.Contains(err.Error(), "configured together") {
		t.Fatalf("incomplete credentials error = %v", err)
	}

	cfg = validTestConfig()
	cfg.Port = 465
	cfg.TLS = false
	if _, _, _, err := validateConfig(cfg, "user@example.com"); err == nil || !strings.Contains(err.Error(), "requires TLS") {
		t.Fatalf("port 465 error = %v", err)
	}
}

func TestBuildMessageSanitizesHeaderNewlines(t *testing.T) {
	cfg := validTestConfig()
	cfg.FromName = "BugPk\r\nBcc: attacker@example.com"
	normalized, from, recipient, err := validateConfig(cfg, "user@example.com")
	if err != nil {
		t.Fatalf("validateConfig() error = %v", err)
	}
	message := buildMessage(normalized, from, recipient, "Code\r\nBcc: attacker@example.com", "body")
	if strings.Contains(message, "\r\nBcc:") || !strings.Contains(message, "BugPk Bcc:") {
		t.Fatalf("unsafe or unexpected message headers: %q", message)
	}
}

func TestSendReturnsCancelledContextBeforeDial(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := Send(ctx, validTestConfig(), "user@example.com", "subject", "body")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Send() error = %v, want context.Canceled", err)
	}
}

func validTestConfig() Config {
	return Config{
		FromEmail: "noreply@example.com",
		FromName:  "BugPk-Api",
		Host:      "smtp.example.com",
		Password:  "secret",
		Port:      587,
		TLS:       true,
		Timeout:   time.Second,
		Username:  "noreply@example.com",
	}
}
