package domainrouting

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"bugpk-api/internal/pkg/requestvalidation"
)

const (
	SourceAuto  = "AUTO"
	SourceBody  = "BODY"
	SourceQuery = "QUERY"

	// DefaultPriority is used when a route document omits priority.
	DefaultPriority = 100

	MatchExact     = "EXACT"
	MatchSubdomain = "SUBDOMAIN"

	ValueModeAll = "ALL"
	ValueModeAny = "ANY"

	FallbackDefaultPool  = "DEFAULT_POOL"
	FallbackDefaultRoute = "DEFAULT_ROUTE"
	FallbackReject       = "REJECT"

	maxRulesPerInterface = 20
	maxDomainsPerRule    = 50
	maxPriority          = 1_000_000
	maxRouteIDRunes      = 64
	maxRouteNameRunes    = 128
	maxMatchFieldRunes   = 128
)

// MatchConfig describes where the business URL is found and how its host is
// compared with the route domain list.
type MatchConfig struct {
	Source    string   `json:"source"`
	Field     string   `json:"field"`
	Mode      string   `json:"mode"`
	ValueMode string   `json:"valueMode"`
	Domains   []string `json:"domains"`
}

// Rule contains only the routing metadata. The owning domain package adds
// the route's polling/upstream fields around this value.
type Rule struct {
	ID       string      `json:"id"`
	Name     string      `json:"name"`
	Enabled  int         `json:"enabled"`
	Priority int         `json:"priority"`
	Match    MatchConfig `json:"match"`
	// PrioritySet lets DTO adapters preserve an explicit priority=0 while
	// still giving an omitted priority the documented default.
	PrioritySet bool `json:"-"`
}

type Selection struct {
	RuleIndex     int
	RuleID        string
	MatchedDomain string
}

// NormalizeRules validates and copies route matching metadata. The caller's
// slice and nested domain slices are never mutated.
func NormalizeRules(input []Rule) ([]Rule, error) {
	if len(input) > maxRulesPerInterface {
		return nil, fmt.Errorf("domain routes cannot contain more than %d entries", maxRulesPerInterface)
	}
	result := make([]Rule, 0, len(input))
	seenIDs := make(map[string]struct{}, len(input))
	for index, item := range input {
		rule := item
		rule.ID = strings.TrimSpace(rule.ID)
		if rule.ID == "" {
			rule.ID = fmt.Sprintf("route-%d", index+1)
		}
		if !validRouteID(rule.ID) {
			return nil, fmt.Errorf("domain route id is invalid")
		}
		if utf8.RuneCountInString(rule.ID) > maxRouteIDRunes {
			return nil, fmt.Errorf("domain route id is too long")
		}
		if _, exists := seenIDs[rule.ID]; exists {
			return nil, fmt.Errorf("domain route id is duplicated")
		}
		seenIDs[rule.ID] = struct{}{}
		if rule.Enabled != 0 && rule.Enabled != 1 {
			return nil, fmt.Errorf("domain route enabled value is invalid")
		}
		if rule.Priority == 0 && !rule.PrioritySet {
			rule.Priority = DefaultPriority
		}
		if rule.Priority < 0 || rule.Priority > maxPriority {
			return nil, fmt.Errorf("domain route priority is invalid")
		}
		rule.PrioritySet = true
		rule.Name = strings.TrimSpace(rule.Name)
		if rule.Name == "" {
			rule.Name = rule.ID
		}
		if utf8.RuneCountInString(rule.Name) > maxRouteNameRunes {
			return nil, fmt.Errorf("domain route name is too long")
		}
		match, err := NormalizeMatchConfig(rule.Match)
		if err != nil {
			return nil, err
		}
		rule.Match = match
		result = append(result, rule)
	}
	return result, nil
}

func NormalizeMatchConfig(input MatchConfig) (MatchConfig, error) {
	result := input
	result.Source = strings.ToUpper(strings.TrimSpace(result.Source))
	if result.Source == "" {
		result.Source = SourceAuto
	}
	if result.Source != SourceAuto && result.Source != SourceQuery && result.Source != SourceBody {
		return MatchConfig{}, fmt.Errorf("domain route match source is invalid")
	}
	result.Field = strings.TrimSpace(result.Field)
	if result.Field == "" {
		result.Field = "url"
	}
	if utf8.RuneCountInString(result.Field) > maxMatchFieldRunes || strings.ContainsAny(result.Field, "\r\n") {
		return MatchConfig{}, fmt.Errorf("domain route match field is invalid")
	}
	result.Mode = strings.ToUpper(strings.TrimSpace(result.Mode))
	if result.Mode == "" {
		result.Mode = MatchSubdomain
	}
	if result.Mode != MatchExact && result.Mode != MatchSubdomain {
		return MatchConfig{}, fmt.Errorf("domain route match mode is invalid")
	}
	result.ValueMode = strings.ToUpper(strings.TrimSpace(result.ValueMode))
	if result.ValueMode == "" {
		result.ValueMode = ValueModeAll
	}
	if result.ValueMode != ValueModeAll && result.ValueMode != ValueModeAny {
		return MatchConfig{}, fmt.Errorf("domain route value mode is invalid")
	}
	domains, err := normalizeDomains(result.Domains)
	if err != nil {
		return MatchConfig{}, err
	}
	result.Domains = domains
	return result, nil
}

func normalizeDomains(input []string) ([]string, error) {
	values := make([]string, 0, len(input))
	for _, raw := range input {
		values = append(values, strings.FieldsFunc(raw, func(r rune) bool {
			return r == ',' || r == ';' || r == '\n' || r == '\r' || r == '\t' || r == ' '
		})...)
	}
	if len(values) == 0 || len(values) > maxDomainsPerRule {
		return nil, fmt.Errorf("domain route must contain 1 to %d domains", maxDomainsPerRule)
	}
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, raw := range values {
		domain, err := requestvalidation.NormalizeDomain(raw)
		if err != nil {
			return nil, fmt.Errorf("domain route domain is invalid: %w", err)
		}
		if _, exists := seen[domain]; exists {
			continue
		}
		seen[domain] = struct{}{}
		result = append(result, domain)
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("domain route must contain at least one domain")
	}
	return result, nil
}

func Select(rules []Rule, input requestvalidation.Input) (Selection, bool) {
	order := make([]int, len(rules))
	for index := range rules {
		order[index] = index
	}
	sort.SliceStable(order, func(left, right int) bool {
		return effectivePriority(rules[order[left]]) < effectivePriority(rules[order[right]])
	})
	for _, index := range order {
		rule := rules[index]
		if rule.Enabled != 1 {
			continue
		}
		match, ok := normalizeRuntimeMatch(rule.Match)
		if !ok {
			continue
		}
		values, found := requestvalidation.ResolveValues(input, requestvalidation.Source(match.Source), match.Field)
		if !found || len(values) == 0 {
			continue
		}
		if match.ValueMode == ValueModeAny {
			for _, value := range values {
				host, valid := requestvalidation.ExtractDomain(value)
				if valid && domainMatches(host, match) {
					return Selection{RuleIndex: index, RuleID: rule.ID, MatchedDomain: host}, true
				}
			}
			continue
		}
		matched := ""
		all := true
		for _, value := range values {
			host, valid := requestvalidation.ExtractDomain(value)
			if !valid || !domainMatches(host, match) {
				all = false
				break
			}
			if matched == "" {
				matched = host
			}
		}
		if all && matched != "" {
			return Selection{RuleIndex: index, RuleID: rule.ID, MatchedDomain: matched}, true
		}
	}
	return Selection{}, false
}

func effectivePriority(rule Rule) int {
	if rule.Priority == 0 && !rule.PrioritySet {
		return DefaultPriority
	}
	return rule.Priority
}

func normalizeRuntimeMatch(input MatchConfig) (MatchConfig, bool) {
	result, err := NormalizeMatchConfig(input)
	return result, err == nil
}

func domainMatches(host string, match MatchConfig) bool {
	for _, candidate := range match.Domains {
		if host == candidate {
			return true
		}
		if match.Mode == MatchSubdomain && strings.HasSuffix(host, "."+candidate) {
			return true
		}
	}
	return false
}

func validRouteID(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range value {
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') &&
			(char < '0' || char > '9') && char != '_' && char != '-' {
			return false
		}
	}
	return true
}
