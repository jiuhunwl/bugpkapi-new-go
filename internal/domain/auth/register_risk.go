package auth

import (
	"context"
	"regexp"
	"strings"
	"time"

	"bugpk-api/internal/pkg/iphash"

	"go.uber.org/zap"
)

// RegisterRiskContext carries server-observable registration signals that are
// not part of RegisterRequest. The client cannot set any of these directly:
// DeviceID comes from a signed HttpOnly cookie, headers come from the request,
// and Elapsed is measured from the server-side captcha issue timestamp.
type RegisterRiskContext struct {
	AcceptLanguage string
	ClientIP       string
	DeviceID       string
	Elapsed        time.Duration
	UserAgent      string
}

const (
	registerDecisionAllow      = "allow"
	registerDecisionQuarantine = "quarantine"
	registerDecisionReject     = "reject"

	registerRiskWindow24h = 24 * time.Hour
	registerRiskWindow30d = 30 * 24 * time.Hour
)

var randomUsernamePattern = regexp.MustCompile(`^[a-z0-9]{8}$`)

var suspiciousUserAgentTokens = []string{
	"curl", "wget", "python", "python-requests", "go-http-client", "java/",
	"okhttp", "node-fetch", "axios", "postmanruntime", "httpclient", "libwww",
}

var commonEmailDomains = map[string]struct{}{
	"qq.com": {}, "gmail.com": {}, "163.com": {}, "126.com": {},
	"outlook.com": {}, "hotmail.com": {}, "foxmail.com": {},
	"yahoo.com": {}, "icloud.com": {}, "live.com": {}, "msn.com": {},
	"sina.com": {}, "sohu.com": {}, "yeah.net": {}, "139.com": {},
	"189.cn": {}, "wo.cn": {}, "aliyun.com": {},
}

type registerRiskAssessment struct {
	ClientIPHash []byte
	Decision     string
	DeviceHash   []byte
	EmailDomain  string
	PrefixHash   []byte
	Signals      []string
	Score        int
}

// assessRegisterRisk evaluates hard per-device/per-prefix caps and then a
// weighted score. Weak signals never decide alone; only a blacklisted domain
// (already rejected earlier) or the configured score threshold rejects.
func (s *Service) assessRegisterRisk(
	ctx context.Context,
	cfg RegisterConfig,
	req RegisterRequest,
	risk RegisterRiskContext,
	elapsed time.Duration,
) (registerRiskAssessment, error) {
	domain, _ := emailDomain(req.Email)
	assessment := registerRiskAssessment{
		ClientIPHash: iphash.RiskDigest(risk.ClientIP),
		Decision:     registerDecisionAllow,
		DeviceHash:   deviceFingerprint(risk.DeviceID),
		EmailDomain:  domain,
		PrefixHash:   iphash.PrefixDigest(risk.ClientIP),
	}
	if !cfg.RegisterRiskEnabled {
		return assessment, nil
	}
	if s == nil || s.repo == nil {
		return assessment, nil
	}
	now := time.Now()
	if s != nil && s.now != nil {
		now = s.now()
	}
	var deviceAccounts, prefixAccounts, domainAccounts int64
	// Risk counts are advisory: a transient read failure must not take
	// registration down. The IP/captcha/email gates still apply.
	if len(assessment.DeviceHash) > 0 && !allZero(assessment.DeviceHash) {
		if count, err := s.repo.CountRegisterAccountsByDevice(ctx, assessment.DeviceHash, now.Add(-registerRiskWindow24h)); err == nil {
			deviceAccounts = count
		} else if s.log != nil {
			s.log.Warn("register risk device count failed", zap.Error(err))
		}
	}
	if count, err := s.repo.CountRegisterAccountsByPrefix(ctx, assessment.PrefixHash, now.Add(-registerRiskWindow24h)); err == nil {
		prefixAccounts = count
	} else if s.log != nil {
		s.log.Warn("register risk prefix count failed", zap.Error(err))
	}
	if domain != "" && !isCommonEmailDomain(domain) {
		if count, err := s.repo.CountRegisterAccountsByDomain(ctx, domain, now.Add(-registerRiskWindow30d)); err == nil {
			domainAccounts = count
		} else if s.log != nil {
			s.log.Warn("register risk domain count failed", zap.Error(err))
		}
	}

	score := 0
	add := func(points int, signal string) {
		score += points
		assessment.Signals = append(assessment.Signals, signal)
	}
	if looksLikeRandomUsername(req.Username, req.Email) {
		add(25, "username_matches_email_random")
	}
	if domain != "" && !isCommonEmailDomain(domain) && domainAccounts == 0 {
		add(15, "first_seen_email_domain")
	}
	userAgent := strings.ToLower(strings.TrimSpace(risk.UserAgent))
	if userAgent == "" {
		add(30, "missing_user_agent")
	} else {
		for _, token := range suspiciousUserAgentTokens {
			if strings.Contains(userAgent, token) {
				add(30, "suspicious_user_agent")
				break
			}
		}
	}
	if strings.TrimSpace(risk.AcceptLanguage) == "" {
		add(10, "missing_accept_language")
	}
	if elapsed > 0 && elapsed < 1500*time.Millisecond {
		add(25, "submit_before_human_speed")
	}
	if risk.DeviceID == "" {
		add(10, "no_device_cookie")
	} else if deviceAccounts > 0 {
		points := int(deviceAccounts) * 15
		if points > 30 {
			points = 30
		}
		add(points, "same_device_reused")
	}
	if prefixAccounts >= 5 {
		add(20, "subnet_registration_burst")
	}

	hardReject := false
	if cfg.RegisterDeviceDayLimit > 0 && risk.DeviceID != "" && deviceAccounts >= int64(cfg.RegisterDeviceDayLimit) {
		hardReject = true
		assessment.Signals = append(assessment.Signals, "device_day_limit_exceeded")
	}
	if cfg.RegisterIPPrefixDayLimit > 0 && prefixAccounts >= int64(cfg.RegisterIPPrefixDayLimit) {
		hardReject = true
		assessment.Signals = append(assessment.Signals, "subnet_day_limit_exceeded")
	}

	quarantineScore := cfg.RegisterRiskQuarantineScore
	rejectScore := cfg.RegisterRiskRejectScore
	if quarantineScore < 0 {
		quarantineScore = 0
	}
	if rejectScore <= quarantineScore {
		rejectScore = quarantineScore + 1
	}
	assessment.Score = score
	switch {
	case hardReject || score >= rejectScore:
		assessment.Decision = registerDecisionReject
	case score >= quarantineScore:
		assessment.Decision = registerDecisionQuarantine
	default:
		assessment.Decision = registerDecisionAllow
	}
	if cfg.RegisterRiskShadowMode {
		if assessment.Decision != registerDecisionAllow {
			assessment.Signals = append(assessment.Signals, "shadow:"+assessment.Decision)
		}
		assessment.Decision = registerDecisionAllow
	}
	return assessment, nil
}

func looksLikeRandomUsername(username, email string) bool {
	username = strings.ToLower(strings.TrimSpace(username))
	if !randomUsernamePattern.MatchString(username) {
		return false
	}
	at := strings.LastIndex(email, "@")
	if at <= 0 {
		return false
	}
	return username == strings.ToLower(strings.TrimSpace(email[:at]))
}

func isCommonEmailDomain(domain string) bool {
	domain = strings.ToLower(strings.TrimSpace(domain))
	if _, ok := commonEmailDomains[domain]; ok {
		return true
	}
	return strings.HasSuffix(domain, ".edu.cn") || strings.HasSuffix(domain, ".edu")
}

func allZero(value []byte) bool {
	for _, b := range value {
		if b != 0 {
			return false
		}
	}
	return true
}

func (s *Service) nowTime() time.Time {
	if s != nil && s.now != nil {
		return s.now()
	}
	return time.Now()
}

// recordRegisterEvent persists the decision stream without blocking the
// registration response on a transient DB failure. The event is evidence for
// tuning, not an authorization gate.
func (s *Service) recordRegisterEvent(
	ctx context.Context,
	assessment registerRiskAssessment,
	req RegisterRequest,
	risk RegisterRiskContext,
	elapsed time.Duration,
	userID int64,
) {
	if s == nil || s.repo == nil {
		return
	}
	eventCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	elapsedMS := int(elapsed.Milliseconds())
	if elapsedMS < 0 {
		elapsedMS = 0
	}
	event := RegisterEvent{
		AcceptLanguage: risk.AcceptLanguage,
		ClientIPHash:   assessment.ClientIPHash,
		Decision:       assessment.Decision,
		DeviceHash:     assessment.DeviceHash,
		ElapsedMS:      elapsedMS,
		EmailDomain:    assessment.EmailDomain,
		PrefixHash:     assessment.PrefixHash,
		Score:          assessment.Score,
		Signals:        assessment.Signals,
		UserAgent:      risk.UserAgent,
		UserID:         userID,
		Username:       req.Username,
	}
	if err := s.repo.CreateRegisterEvent(eventCtx, event); err != nil && s.log != nil {
		s.log.Warn("record register event failed",
			zap.String("decision", event.Decision),
			zap.Int("score", event.Score),
			zap.Error(err),
		)
	}
}
