package auth

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

// RegisterEvent is one registration decision. It intentionally contains no
// plaintext IP: client/prefix hashes are produced by iphash and the device hash
// is a digest of a random signed-cookie id.
type RegisterEvent struct {
	AcceptLanguage string
	ClientIPHash   []byte
	Decision       string
	DeviceHash     []byte
	ElapsedMS      int
	EmailDomain    string
	PrefixHash     []byte
	Score          int
	Signals        []string
	UserAgent      string
	UserID         int64
	Username       string
}

func (r *Repository) CreateRegisterEvent(ctx context.Context, event RegisterEvent) error {
	clientHash := binary32(event.ClientIPHash)
	prefixHash := binary32(event.PrefixHash)
	deviceHash := binary32(event.DeviceHash)
	signals, err := json.Marshal(event.Signals)
	if err != nil {
		signals = []byte("[]")
	}
	var userID any
	if event.UserID > 0 {
		userID = event.UserID
	}
	_, err = r.db.ExecContext(ctx, `
		insert into bp_register_event(
			decision, score, user_id, username, email_domain, client_ip_hash,
			prefix_hash, device_hash, user_agent, accept_language, elapsed_ms, signals
		) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, event.Decision, event.Score, userID, clipRunes(event.Username, 64),
		clipRunes(event.EmailDomain, 255), clientHash, prefixHash, deviceHash,
		clipRunes(event.UserAgent, 512), clipRunes(event.AcceptLanguage, 128),
		event.ElapsedMS, signals)
	return err
}

func (r *Repository) CountRegisterAccountsByDevice(ctx context.Context, hash []byte, since time.Time) (int64, error) {
	if len(hash) == 0 {
		return 0, nil
	}
	return r.countRegisterRiskRows(ctx, "device_hash", hash, since)
}

func (r *Repository) CountRegisterAccountsByPrefix(ctx context.Context, hash []byte, since time.Time) (int64, error) {
	if len(hash) == 0 {
		return 0, nil
	}
	return r.countRegisterRiskRows(ctx, "prefix_hash", hash, since)
}

func (r *Repository) countRegisterRiskRows(ctx context.Context, column string, hash []byte, since time.Time) (int64, error) {
	var count int64
	err := r.db.QueryRowContext(ctx, `
		select count(*)
		from bp_register_event
		where `+column+` = ?
		  and created_at >= ?
		  and decision in ('allow', 'quarantine')
	`, binary32(hash), since).Scan(&count)
	return count, err
}

func (r *Repository) CountRegisterAccountsByDomain(ctx context.Context, domain string, since time.Time) (int64, error) {
	domain = strings.ToLower(strings.TrimSpace(domain))
	if domain == "" {
		return 0, nil
	}
	var count int64
	err := r.db.QueryRowContext(ctx, `
		select count(*)
		from bp_register_event
		where email_domain = ?
		  and created_at >= ?
		  and decision in ('allow', 'quarantine')
	`, domain, since).Scan(&count)
	return count, err
}

func (r *Repository) SetUserQuarantine(ctx context.Context, userID int64, until time.Time, score int, reason string) error {
	_, err := r.db.ExecContext(ctx, `
		insert into bp_user_risk_state(user_id, quarantine_until, risk_score, reason)
		values (?, ?, ?, ?)
		on duplicate key update
			quarantine_until = values(quarantine_until),
			risk_score = values(risk_score),
			reason = values(reason)
	`, userID, until, score, clipRunes(reason, 255))
	return err
}

func binary32(value []byte) []byte {
	if len(value) == 32 {
		return value
	}
	normalized := make([]byte, 32)
	copy(normalized, value)
	return normalized
}

func clipRunes(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}
