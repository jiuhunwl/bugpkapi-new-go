package smtpmail

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"strings"
	"time"
)

type Config struct {
	FromEmail string
	FromName  string
	Host      string
	Password  string
	Port      int
	TLS       bool
	Timeout   time.Duration
	Username  string
}

func Send(ctx context.Context, cfg Config, to, subject, body string) error {
	normalized, from, recipient, err := validateConfig(cfg, to)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	addr := net.JoinHostPort(normalized.Host, fmt.Sprintf("%d", normalized.Port))
	dialer := net.Dialer{Timeout: normalized.Timeout}
	var conn net.Conn
	if normalized.TLS && normalized.Port == 465 {
		conn, err = tls.DialWithDialer(&dialer, "tcp", addr, tlsConfig(normalized.Host))
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return err
	}
	deadline := time.Now().Add(normalized.Timeout)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	if err := conn.SetDeadline(deadline); err != nil {
		_ = conn.Close()
		return err
	}

	client, err := smtp.NewClient(conn, normalized.Host)
	if err != nil {
		_ = conn.Close()
		return err
	}
	defer client.Close()

	if normalized.TLS && normalized.Port != 465 {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return errors.New("smtp server does not support STARTTLS")
		}
		if err := client.StartTLS(tlsConfig(normalized.Host)); err != nil {
			return err
		}
	}
	if normalized.Username != "" {
		auth := smtp.PlainAuth("", normalized.Username, normalized.Password, normalized.Host)
		if err := client.Auth(auth); err != nil {
			return err
		}
	}
	if err := client.Mail(from.Address); err != nil {
		return err
	}
	if err := client.Rcpt(recipient.Address); err != nil {
		return err
	}
	writer, err := client.Data()
	if err != nil {
		return err
	}
	message := buildMessage(normalized, from, recipient, subject, body)
	if _, err := writer.Write([]byte(message)); err != nil {
		_ = writer.Close()
		return err
	}
	return writer.Close()
}

func validateConfig(cfg Config, to string) (Config, *mail.Address, *mail.Address, error) {
	cfg.Host = strings.TrimSpace(cfg.Host)
	cfg.Username = strings.TrimSpace(cfg.Username)
	cfg.FromEmail = strings.ToLower(strings.TrimSpace(cfg.FromEmail))
	cfg.FromName = sanitizeHeader(cfg.FromName)
	if cfg.Timeout <= 0 {
		cfg.Timeout = 10 * time.Second
	}
	if cfg.Host == "" || strings.ContainsAny(cfg.Host, "\r\n/\\") {
		return Config{}, nil, nil, errors.New("smtp host is invalid")
	}
	if cfg.Port < 1 || cfg.Port > 65535 {
		return Config{}, nil, nil, errors.New("smtp port must be between 1 and 65535")
	}
	if cfg.Timeout < 100*time.Millisecond || cfg.Timeout > time.Minute {
		return Config{}, nil, nil, errors.New("smtp timeout must be between 100ms and 60s")
	}
	if cfg.Port == 465 && !cfg.TLS {
		return Config{}, nil, nil, errors.New("smtp port 465 requires TLS")
	}
	if (cfg.Username == "") != (cfg.Password == "") {
		return Config{}, nil, nil, errors.New("smtp username and password must be configured together")
	}
	if cfg.Username != "" && !cfg.TLS && !isLoopbackHost(cfg.Host) {
		return Config{}, nil, nil, errors.New("smtp authentication requires TLS for non-loopback servers")
	}
	from, err := mail.ParseAddress(cfg.FromEmail)
	if err != nil || !strings.EqualFold(from.Address, cfg.FromEmail) {
		return Config{}, nil, nil, errors.New("smtp from address is invalid")
	}
	recipient, err := mail.ParseAddress(strings.TrimSpace(to))
	if err != nil || recipient.Address == "" {
		return Config{}, nil, nil, errors.New("smtp recipient address is invalid")
	}
	return cfg, from, recipient, nil
}

func buildMessage(cfg Config, from, to *mail.Address, subject, body string) string {
	fromHeader := (&mail.Address{Name: cfg.FromName, Address: from.Address}).String()
	encodedSubject := mime.QEncoding.Encode("utf-8", sanitizeHeader(subject))
	return strings.Join([]string{
		"From: " + fromHeader,
		"To: " + to.String(),
		"Subject: " + encodedSubject,
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=UTF-8",
		"Content-Transfer-Encoding: 8bit",
		"",
		body,
	}, "\r\n")
}

func sanitizeHeader(value string) string {
	return strings.Join(strings.Fields(strings.NewReplacer("\r", " ", "\n", " ").Replace(value)), " ")
}

func tlsConfig(host string) *tls.Config {
	return &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(strings.TrimSpace(host), "localhost") {
		return true
	}
	ip := net.ParseIP(strings.TrimSpace(host))
	return ip != nil && ip.IsLoopback()
}
