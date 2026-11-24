package emailpolicy

import "testing"

func TestDomain(t *testing.T) {
	cases := []struct {
		email  string
		domain string
		ok     bool
	}{
		{"poc_fixed@example.com", "example.com", true},
		{"A@B.CoM", "b.com", true},
		{"no-at-sign", "", false},
		{"trailing@", "", false},
		{"", "", false},
	}
	for _, tc := range cases {
		domain, ok := Domain(tc.email)
		if domain != tc.domain || ok != tc.ok {
			t.Fatalf("Domain(%q) = (%q, %v), want (%q, %v)", tc.email, domain, ok, tc.domain, tc.ok)
		}
	}
}

func TestMatchesWildcardOnlyMatchesSubdomain(t *testing.T) {
	rules := []string{"*.example.com"}
	if Matches(rules, "example.com") {
		t.Fatal("wildcard rule must not match the bare domain")
	}
	if !Matches(rules, "a.example.com") {
		t.Fatal("wildcard rule must match subdomain")
	}
	if Matches([]string{" *.example.com "}, "a.example.com") != true {
		t.Fatal("rules with surrounding spaces must still match")
	}
}

func TestEvaluate(t *testing.T) {
	whitelist := []string{"qq.com", "163.com"}
	blacklist := []string{"*.mailinator.com", "tempmail.com"}

	if got := Evaluate(whitelist, blacklist, "a@qq.com", true); !got.Allowed {
		t.Fatalf("qq.com should pass strict policy: %#v", got)
	}
	if got := Evaluate(whitelist, blacklist, "a@example.com", true); got.Allowed || got.Rule != "whitelist" {
		t.Fatalf("example.com must be rejected by whitelist: %#v", got)
	}
	if got := Evaluate(whitelist, blacklist, "a@x.mailinator.com", true); got.Allowed || got.Rule != "blacklist" {
		t.Fatalf("blacklist must win over whitelist: %#v", got)
	}
	// 非严格模式只挡黑名单，避免历史绑定地址被新白名单锁死。
	if got := Evaluate(whitelist, blacklist, "a@example.com", false); !got.Allowed {
		t.Fatalf("non-strict mode must allow non-blacklisted domain: %#v", got)
	}
	if got := Evaluate(whitelist, blacklist, "a@tempmail.com", false); got.Allowed {
		t.Fatalf("non-strict mode must still reject blacklist: %#v", got)
	}
	// 空白名单 = 不限制域名（只受黑名单约束）。
	if got := Evaluate(nil, blacklist, "a@example.com", true); !got.Allowed {
		t.Fatalf("empty whitelist must not restrict: %#v", got)
	}
	if got := Evaluate(whitelist, blacklist, "not-an-email", true); got.Allowed || got.Rule != "unparsable" {
		t.Fatalf("unparsable address must be rejected: %#v", got)
	}
}
