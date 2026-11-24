package requestvalidation

import (
	"strings"
	"testing"
)

func TestNormalizePolicyNormalizesDomainRulesAndOrdering(t *testing.T) {
	policy, err := Normalize(Policy{
		DefaultResult: "platform_mismatch",
		Enabled:       1,
		Rules: []Rule{{
			Conditions: []Condition{{
				ExpectedValue: "https://DouYin.com/path; v.douyin.com",
				FieldName:     " url ",
				Operator:      "domain_in",
				Source:        "auto",
			}},
			Logic:  "all",
			Name:   " 平台匹配 ",
			Result: "pass",
			SortNo: 99,
		}},
	})
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	if policy.DefaultResult != ResultPlatformMismatch || policy.Rules[0].SortNo != 1 {
		t.Fatalf("unexpected normalized policy: %#v", policy)
	}
	condition := policy.Rules[0].Conditions[0]
	if condition.ExpectedValue != "douyin.com,v.douyin.com" || condition.FieldName != "url" {
		t.Fatalf("unexpected normalized condition: %#v", condition)
	}
}

func TestNormalizePolicyRejectsUnsafeRules(t *testing.T) {
	tests := []Policy{
		{Enabled: 2},
		{Enabled: 1, DefaultResult: "UNKNOWN"},
		{Enabled: 1, Rules: []Rule{{Logic: LogicAll, Result: ResultPass}}},
		{Enabled: 1, Rules: []Rule{{Logic: "XOR", Result: ResultPass, Conditions: []Condition{{FieldName: "url", Operator: OperatorExists}}}}},
		{Enabled: 1, Rules: []Rule{{Logic: LogicAll, Result: ResultPass, Conditions: []Condition{{FieldName: "url", Operator: OperatorRegex, ExpectedValue: "("}}}}},
		{Enabled: 1, Rules: []Rule{{Logic: LogicAll, Result: ResultPass, Conditions: []Condition{{FieldName: "url", Operator: OperatorDomainIn, ExpectedValue: "localhost"}}}}},
	}
	for index, policy := range tests {
		if _, err := Normalize(policy); err == nil {
			t.Fatalf("case %d: expected policy to be rejected: %#v", index, policy)
		}
	}
}

func TestEvaluateShareLinkPlatformPreset(t *testing.T) {
	policy := mustNormalize(t, Policy{
		DefaultResult: ResultPlatformMismatch,
		Enabled:       1,
		Rules: []Rule{
			{
				Name:   "参数不合规",
				Logic:  LogicAny,
				Result: ResultInvalidParams,
				Conditions: []Condition{
					{Source: SourceAuto, FieldName: "url", Operator: OperatorNotExists},
					{Source: SourceAuto, FieldName: "url", Operator: OperatorEmpty},
					{Source: SourceAuto, FieldName: "url", Operator: OperatorNotHasDomain},
				},
			},
			{
				Name:   "正常解析",
				Logic:  LogicAll,
				Result: ResultPass,
				Conditions: []Condition{{
					Source: SourceAuto, FieldName: "url", Operator: OperatorDomainIn,
					ExpectedValue: "douyin.com,iesdouyin.com",
				}},
			},
		},
	})

	tests := []struct {
		name   string
		query  map[string][]string
		result Result
	}{
		{name: "missing", query: map[string][]string{}, result: ResultInvalidParams},
		{name: "empty", query: map[string][]string{"url": {""}}, result: ResultInvalidParams},
		{name: "no domain", query: map[string][]string{"url": {"复制这段口令打开应用"}}, result: ResultInvalidParams},
		{name: "matching embedded share link", query: map[string][]string{"url": {"复制打开抖音 https://v.douyin.com/abc/ 查看视频"}}, result: ResultPass},
		{name: "different platform", query: map[string][]string{"url": {"https://www.kuaishou.com/short-video/abc"}}, result: ResultPlatformMismatch},
		{name: "duplicate includes mismatch", query: map[string][]string{"url": {"https://v.douyin.com/abc", "https://www.kuaishou.com/abc"}}, result: ResultPlatformMismatch},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			decision := Evaluate(policy, Input{Query: tt.query})
			if decision.Result != tt.result {
				t.Fatalf("Evaluate() result = %s, want %s (rule=%q)", decision.Result, tt.result, decision.RuleName)
			}
		})
	}
}

func TestEvaluateSupportsBodyPathsAndAnyLogic(t *testing.T) {
	policy := mustNormalize(t, Policy{
		DefaultResult: ResultPass,
		Enabled:       1,
		Rules: []Rule{{
			Name:   "body invalid",
			Logic:  LogicAny,
			Result: ResultInvalidParams,
			Conditions: []Condition{
				{Source: SourceBody, FieldName: "payload.url", Operator: OperatorNotHasDomain},
				{Source: SourceBody, FieldName: "payload.platform", Operator: OperatorNotEqual, ExpectedValue: "douyin"},
			},
		}},
	})

	decision := Evaluate(policy, Input{
		Body:        []byte(`{"payload":{"url":"https://v.douyin.com/abc","platform":"kuaishou"}}`),
		ContentType: "application/json",
	})
	if decision.Result != ResultInvalidParams || decision.RuleName != "body invalid" {
		t.Fatalf("unexpected decision: %#v", decision)
	}
}

func TestExtractDomainSupportsShareTextAndBareDomains(t *testing.T) {
	for _, value := range []string{
		"https://v.douyin.com/abc",
		"复制打开 https://b23.tv/abcd 查看",
		"www.xiaohongshu.com/explore/123",
	} {
		host, ok := extractDomain(value)
		if !ok || !strings.Contains(host, ".") {
			t.Fatalf("extractDomain(%q) = %q, %v", value, host, ok)
		}
	}
}

func TestExtractDomainRejectsURLsWithUserInfo(t *testing.T) {
	for _, value := range []string{
		"https://user:password@example.com/resource",
		"复制打开 https://user:password@example.com/resource 查看",
		"http://user@example.com/resource",
		"user:password@example.com/resource",
	} {
		if host, ok := ExtractDomain(value); ok {
			t.Fatalf("ExtractDomain(%q) = %q, true; URLs with user-info must be invalid", value, host)
		}
	}
}

func mustNormalize(t *testing.T, policy Policy) Policy {
	t.Helper()
	normalized, err := Normalize(policy)
	if err != nil {
		t.Fatal(err)
	}
	return normalized
}

func TestExtractDomainDoesNotUsePathAsInvalidURLHost(t *testing.T) {
	for _, value := range []string{
		"https://192.0.2.1/v.douyin.com/a",
		"https://:443/v.douyin.com/a",
		"复制打开 https://192.0.2.1/v.douyin.com/a 查看",
		"https://[2001:db8::1]/v.douyin.com/a",
		"https://localhost/v.douyin.com/a",
		"https://bad_host.test/v.douyin.com/a",
	} {
		if host, ok := ExtractDomain(value); ok {
			t.Errorf("ExtractDomain(%q) = %q, true; path must not replace the URL host", value, host)
		}
	}
}

func TestExtractDomainAllowsAtSignOutsideURLAuthority(t *testing.T) {
	for _, value := range []string{
		"https://www.tiktok.com/@alice.smith/video/123",
		"复制打开 https://www.tiktok.com/@alice.smith/video/123 查看",
		"https://www.tiktok.com/video/123?owner=@alice.smith",
		"https://www.tiktok.com/video/123#@alice.smith",
	} {
		if host, ok := ExtractDomain(value); !ok || host != "www.tiktok.com" {
			t.Errorf("ExtractDomain(%q) = %q, %v", value, host, ok)
		}
	}
}

func TestEvaluateAutoChecksAllSameNamedQueryAndBodyValues(t *testing.T) {
	policy := mustNormalize(t, Policy{Enabled: 1, DefaultResult: ResultPlatformMismatch, Rules: []Rule{{
		Name: "platform allowlist", Logic: LogicAll, Result: ResultPass,
		Conditions: []Condition{{Source: SourceAuto, FieldName: "url", Operator: OperatorDomainIn, ExpectedValue: "douyin.com"}},
	}}})
	tests := []struct {
		name        string
		body        string
		contentType string
		query       map[string][]string
		want        Result
	}{
		{"conflicting JSON body", `{"url":"https://www.kuaishou.com/a"}`, "application/json", map[string][]string{"url": {"https://v.douyin.com/a"}}, ResultPlatformMismatch},
		{"conflicting form body", "url=https%3A%2F%2Fwww.kuaishou.com%2Fa", "application/x-www-form-urlencoded", map[string][]string{"url": {"https://v.douyin.com/a"}}, ResultPlatformMismatch},
		{"empty body array", `{"url":[]}`, "application/json", map[string][]string{"url": {"https://v.douyin.com/a"}}, ResultPlatformMismatch},
		{"same platform both sources", `{"url":"https://www.douyin.com/b"}`, "application/json", map[string][]string{"url": {"https://v.douyin.com/a"}}, ResultPass},
		{"query only", "", "", map[string][]string{"url": {"https://v.douyin.com/a"}}, ResultPass},
		{"body only", `{"url":"https://v.douyin.com/a"}`, "application/json", nil, ResultPass},
		{"path disguises IP host", "", "", map[string][]string{"url": {"https://192.0.2.1/v.douyin.com/a"}}, ResultPlatformMismatch},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := Input{Body: []byte(tt.body), ContentType: tt.contentType, Query: tt.query}
			if got := Evaluate(policy, input); got.Result != tt.want {
				t.Fatalf("Evaluate() = %+v, want %s", got, tt.want)
			}
		})
	}
}
