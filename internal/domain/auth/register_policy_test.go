package auth

import (
	"testing"
	"time"
)

func TestResolveOAuthRegisterPolicyUsesProviderOverrides(t *testing.T) {
	cfg := RegisterConfig{
		DefaultRoleID:              2,
		RegisterGiftPoints:         10,
		OAuthRegisterEnabled:       true,
		OAuthRegisterModeDefault:   OAuthRegisterModeConfirm,
		OAuthRegisterIPHourLimit:   5,
		OAuthRegisterIPDayLimit:    20,
		OAuthRegisterIPMaxAccounts: 30,
		OAuthRequireVerifiedEmail:  true,
	}
	provider := OAuthProviderConfig{
		ProviderKey:              "github",
		RegisterMode:             OAuthRegisterModeInherit,
		DefaultRoleID:            7,
		RegisterGiftPoints:       100,
		RegisterIPHourLimit:      1,
		RegisterIPDayLimit:       2,
		RegisterIPMaxAccounts:    3,
		RequireVerifiedEmailMode: 0,
		MinAccountAgeDays:        30,
		MinTrustLevel:            2,
		RequireActive:            false,
		RejectSilenced:           true,
	}
	policy, err := resolveOAuthRegisterPolicy(cfg, provider)
	if err != nil {
		t.Fatal(err)
	}
	if policy.Mode != OAuthRegisterModeConfirm || !policy.Enabled {
		t.Fatalf("unexpected mode/enabled: %+v", policy)
	}
	if policy.DefaultRoleID != 7 || policy.GiftPoints != 100 {
		t.Fatalf("role/gift override not applied: %+v", policy)
	}
	if policy.IPHourLimit != 1 || policy.IPDayLimit != 2 || policy.IPMaxAccounts != 3 {
		t.Fatalf("IP overrides not applied: %+v", policy)
	}
	if policy.RequireVerifiedEmail {
		t.Fatalf("explicit provider email policy 0 should disable global requirement: %+v", policy)
	}
	if policy.MinAccountAgeDays != 30 || policy.MinTrustLevel != 2 || policy.RequireActive || !policy.RejectSilenced {
		t.Fatalf("risk gates not applied: %+v", policy)
	}
}

func TestResolveOAuthRegisterPolicyInheritsGlobal(t *testing.T) {
	cfg := RegisterConfig{
		DefaultRoleID:              2,
		RegisterGiftPoints:         10,
		OAuthRegisterEnabled:       true,
		OAuthRegisterModeDefault:   OAuthRegisterModeAuto,
		OAuthRegisterIPHourLimit:   5,
		OAuthRegisterIPDayLimit:    20,
		OAuthRegisterIPMaxAccounts: 30,
		OAuthRequireVerifiedEmail:  true,
	}
	policy, err := resolveOAuthRegisterPolicy(cfg, OAuthProviderConfig{
		ProviderKey:              "linuxdo",
		RegisterGiftPoints:       -1,
		RegisterIPHourLimit:      -1,
		RegisterIPDayLimit:       -1,
		RegisterIPMaxAccounts:    -1,
		RequireVerifiedEmailMode: -1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if policy.Mode != OAuthRegisterModeAuto || policy.DefaultRoleID != 2 ||
		policy.GiftPoints != 10 || policy.IPHourLimit != 5 || policy.IPDayLimit != 20 ||
		policy.IPMaxAccounts != 30 || !policy.RequireVerifiedEmail {
		t.Fatalf("global defaults not inherited: %+v", policy)
	}
}

func TestCheckOAuthRiskGates(t *testing.T) {
	svc := &OAuthService{now: func() time.Time { return time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC) }}
	identity := OAuthIdentity{
		Active:           true,
		ActiveSet:        true,
		TrustLevel:       2,
		AccountCreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	policy := RegisterPolicy{RequireActive: true, RejectSilenced: true, MinTrustLevel: 2, MinAccountAgeDays: 30}
	if err := svc.checkOAuthRisk(policy, identity); err != nil {
		t.Fatalf("valid identity rejected: %v", err)
	}
	identity.Active = false
	if err := svc.checkOAuthRisk(policy, identity); err == nil {
		t.Fatal("inactive identity accepted")
	}
	identity.Active = true
	identity.Silenced = true
	if err := svc.checkOAuthRisk(policy, identity); err == nil {
		t.Fatal("silenced identity accepted")
	}
	identity.Silenced = false
	identity.TrustLevel = 1
	if err := svc.checkOAuthRisk(policy, identity); err == nil {
		t.Fatal("low trust identity accepted")
	}
	identity.TrustLevel = 2
	identity.AccountCreatedAt = svc.now().AddDate(0, 0, -10)
	if err := svc.checkOAuthRisk(policy, identity); err == nil {
		t.Fatal("young account accepted")
	}
}
