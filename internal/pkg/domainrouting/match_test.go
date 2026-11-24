package domainrouting

import (
	"testing"

	"bugpk-api/internal/pkg/requestvalidation"
)

func TestNormalizeRulesAndSelectByPriorityAndDomainMode(t *testing.T) {
	rules, err := NormalizeRules([]Rule{
		{
			Name:     "wide",
			Enabled:  1,
			Priority: 100,
			Match: MatchConfig{
				Source:    SourceQuery,
				Field:     "url",
				Mode:      MatchSubdomain,
				ValueMode: ValueModeAll,
				Domains:   []string{"example.com"},
			},
		},
		{
			ID:       "exact",
			Name:     "exact host",
			Enabled:  1,
			Priority: 10,
			Match: MatchConfig{
				Source:    SourceQuery,
				Field:     "url",
				Mode:      MatchExact,
				ValueMode: ValueModeAll,
				Domains:   []string{"api.example.com"},
			},
		},
	})
	if err != nil {
		t.Fatalf("NormalizeRules() error = %v", err)
	}
	if rules[0].ID != "route-1" || rules[0].Match.Source != SourceQuery {
		t.Fatalf("normalized generated rule = %+v", rules[0])
	}

	selection, ok := Select(rules, requestvalidation.Input{Query: map[string][]string{
		"url": {"https://api.example.com/resource"},
	}})
	if !ok || selection.RuleID != "exact" || selection.MatchedDomain != "api.example.com" {
		t.Fatalf("selection = %+v, ok=%v", selection, ok)
	}

	selection, ok = Select(rules, requestvalidation.Input{Query: map[string][]string{
		"url": {"https://www.example.com/resource"},
	}})
	if !ok || selection.RuleID != "route-1" || selection.MatchedDomain != "www.example.com" {
		t.Fatalf("subdomain selection = %+v, ok=%v", selection, ok)
	}

	if _, ok := Select(rules, requestvalidation.Input{Query: map[string][]string{
		"url": {"https://another.example.net/resource"},
	}}); ok {
		t.Fatal("different domain unexpectedly matched")
	}
}

func TestSelectSupportsBodyPathAndAnyOrAllValues(t *testing.T) {
	rules, err := NormalizeRules([]Rule{
		{
			ID:       "any-route",
			Enabled:  1,
			Priority: 1,
			Match: MatchConfig{
				Source:    SourceBody,
				Field:     "payload.urls",
				Mode:      MatchSubdomain,
				ValueMode: ValueModeAny,
				Domains:   []string{"douyin.com"},
			},
		},
	})
	if err != nil {
		t.Fatalf("NormalizeRules() error = %v", err)
	}
	selection, ok := Select(rules, requestvalidation.Input{
		Body:        []byte(`{"payload":{"urls":["https://other.example/a","https://v.douyin.com/b"]}}`),
		ContentType: "application/json",
	})
	if !ok || selection.RuleID != "any-route" || selection.MatchedDomain != "v.douyin.com" {
		t.Fatalf("ANY selection = %+v, ok=%v", selection, ok)
	}

	rules[0].Match.ValueMode = ValueModeAll
	if _, ok := Select(rules, requestvalidation.Input{
		Body:        []byte(`{"payload":{"urls":["https://other.example/a","https://v.douyin.com/b"]}}`),
		ContentType: "application/json",
	}); ok {
		t.Fatal("ALL selection matched a mixed-domain request")
	}
}

func TestNormalizeRulesRejectsInvalidAndDuplicateConfiguration(t *testing.T) {
	cases := []Rule{
		{ID: "same", Enabled: 1, Match: MatchConfig{Domains: []string{"example.com"}}},
		{ID: "same", Enabled: 1, Match: MatchConfig{Domains: []string{"other.com"}}},
	}
	if _, err := NormalizeRules(cases); err == nil {
		t.Fatal("duplicate route IDs were accepted")
	}
	if _, err := NormalizeRules([]Rule{{
		ID:      "bad-domain",
		Enabled: 1,
		Match:   MatchConfig{Domains: []string{"127.0.0.1"}},
	}}); err == nil {
		t.Fatal("IP domain was accepted")
	}
	if _, err := NormalizeRules([]Rule{{
		ID:      "bad-mode",
		Enabled: 1,
		Match: MatchConfig{
			Mode:    "NOT_A_MODE",
			Domains: []string{"example.com"},
		},
	}}); err == nil {
		t.Fatal("invalid match mode was accepted")
	}
}

func TestSelectKeepsStableOrderForEqualPriority(t *testing.T) {
	rules, err := NormalizeRules([]Rule{
		{ID: "first", Enabled: 1, Priority: 5, Match: MatchConfig{Domains: []string{"example.com"}}},
		{ID: "second", Enabled: 1, Priority: 5, Match: MatchConfig{Domains: []string{"example.com"}}},
	})
	if err != nil {
		t.Fatalf("NormalizeRules() error = %v", err)
	}
	selection, ok := Select(rules, requestvalidation.Input{Query: map[string][]string{
		"url": {"https://www.example.com/a"},
	}})
	if !ok || selection.RuleID != "first" {
		t.Fatalf("equal-priority selection = %+v, ok=%v", selection, ok)
	}
}

func TestSelectSupportsFormBodyValues(t *testing.T) {
	rules, err := NormalizeRules([]Rule{{
		ID:      "form-route",
		Enabled: 1,
		Match: MatchConfig{
			Source:    SourceBody,
			Field:     "url",
			Mode:      MatchSubdomain,
			ValueMode: ValueModeAll,
			Domains:   []string{"example.com"},
		},
	}})
	if err != nil {
		t.Fatalf("NormalizeRules() error = %v", err)
	}
	selection, ok := Select(rules, requestvalidation.Input{
		Body:        []byte("url=https%3A%2F%2Fv.example.com%2Fresource"),
		ContentType: "application/x-www-form-urlencoded; charset=utf-8",
	})
	if !ok || selection.RuleID != "form-route" {
		t.Fatalf("form selection = %+v, ok=%v", selection, ok)
	}
}
