package auth

import (
	"strings"
	"testing"
)

func TestHoneypotTripped(t *testing.T) {
	cases := []struct {
		name string
		req  RegisterRequest
		want bool
	}{
		{name: "empty", req: RegisterRequest{Website: ""}, want: false},
		{name: "blank", req: RegisterRequest{Website: "   "}, want: false},
		{name: "filled", req: RegisterRequest{Website: "https://spam.example"}, want: true},
		{name: "filled spaces", req: RegisterRequest{Website: " x "}, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := honeypotTripped(tc.req); got != tc.want {
				t.Fatalf("honeypotTripped(%q) = %v, want %v", tc.req.Website, got, tc.want)
			}
		})
	}
}

func TestEmailDomain(t *testing.T) {
	cases := []struct {
		email  string
		domain string
		ok     bool
	}{
		{email: "user@example.com", domain: "example.com", ok: true},
		{email: "USER@Example.COM", domain: "example.com", ok: true},
		{email: "a+b@sub.example.com", domain: "sub.example.com", ok: true},
		{email: "no-at-sign", domain: "", ok: false},
		{email: "trailing@", domain: "", ok: false},
		{email: "", domain: "", ok: false},
	}
	for _, tc := range cases {
		domain, ok := emailDomain(tc.email)
		if domain != tc.domain || ok != tc.ok {
			t.Fatalf("emailDomain(%q) = (%q, %v), want (%q, %v)", tc.email, domain, ok, tc.domain, tc.ok)
		}
	}
}

func TestFakeTokenPairShape(t *testing.T) {
	pair := fakeTokenPair()
	if pair.AccessToken == "" || pair.RefreshToken == "" {
		t.Fatal("fake tokens must not be empty")
	}
	for _, token := range []string{pair.AccessToken, pair.RefreshToken} {
		if segments := strings.Split(token, "."); len(segments) != 3 {
			t.Fatalf("fake token %q does not look like a JWT (segments=%d)", token, len(segments))
		}
	}
	if pair.RefreshJTI == "" {
		t.Fatal("fake refresh JTI must not be empty")
	}
	// Two pairs must never be identical: replaying one must not look like a
	// valid session for another bot.
	if pair.AccessToken == fakeTokenPair().AccessToken {
		t.Fatal("fake access tokens must be random per call")
	}
}

func TestDomainRuleMatches(t *testing.T) {
	rules := []string{"example.com", "*.mail.example", "blocked.org"}
	cases := []struct {
		domain string
		want   bool
	}{
		{domain: "example.com", want: true},
		{domain: "EXAMPLE.COM", want: true},
		{domain: "sub.example.com", want: false},    // exact rules do not match sub-domains
		{domain: "a.mail.example", want: true},      // wildcard matches sub-domains
		{domain: "deep.a.mail.example", want: true}, // wildcard matches deeper sub-domains
		{domain: "mail.example", want: false},       // wildcard does not match the bare domain
		{domain: "blocked.org", want: true},
		{domain: "other.org", want: false},
	}
	for _, tc := range cases {
		if got := domainRuleMatches(rules, strings.ToLower(tc.domain)); got != tc.want {
			t.Fatalf("domainRuleMatches(rules, %q) = %v, want %v", tc.domain, got, tc.want)
		}
	}
}
