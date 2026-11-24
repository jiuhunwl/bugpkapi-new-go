package requestvalidation

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"bugpk-api/internal/pkg/jsonrequest"
)

type Result string

const (
	ResultPass             Result = "PASS"
	ResultInvalidParams    Result = "INVALID_PARAMS"
	ResultPlatformMismatch Result = "PLATFORM_MISMATCH"
)

type Logic string

const (
	LogicAll Logic = "ALL"
	LogicAny Logic = "ANY"
)

type Source string

const (
	SourceAuto  Source = "AUTO"
	SourceBody  Source = "BODY"
	SourceQuery Source = "QUERY"
)

type Operator string

const (
	OperatorContains     Operator = "CONTAINS"
	OperatorDomainIn     Operator = "DOMAIN_IN"
	OperatorDomainNotIn  Operator = "DOMAIN_NOT_IN"
	OperatorEmpty        Operator = "EMPTY"
	OperatorEndsWith     Operator = "ENDS_WITH"
	OperatorEqual        Operator = "EQ"
	OperatorExists       Operator = "EXISTS"
	OperatorHasDomain    Operator = "HAS_DOMAIN"
	OperatorIsURL        Operator = "IS_URL"
	OperatorNotContains  Operator = "NOT_CONTAINS"
	OperatorNotEmpty     Operator = "NOT_EMPTY"
	OperatorNotEqual     Operator = "NE"
	OperatorNotExists    Operator = "NOT_EXISTS"
	OperatorNotHasDomain Operator = "NOT_HAS_DOMAIN"
	OperatorNotRegex     Operator = "NOT_REGEX"
	OperatorNotURL       Operator = "NOT_URL"
	OperatorRegex        Operator = "REGEX"
	OperatorStartsWith   Operator = "STARTS_WITH"
)

const (
	MaxConditionsPerRule = 10
	MaxExpectedRunes     = 1024
	MaxFieldRunes        = 128
	MaxNameRunes         = 64
	MaxRegexRunes        = 256
	MaxRules             = 20
	maxJSONDepth         = 64
	maxPlatformDomains   = 20
)

type Policy struct {
	DefaultResult Result `json:"defaultResult"`
	Enabled       int    `json:"enabled"`
	Rules         []Rule `json:"rules"`
}

type Rule struct {
	Conditions []Condition `json:"conditions"`
	Logic      Logic       `json:"logic"`
	Name       string      `json:"name"`
	Result     Result      `json:"result"`
	SortNo     int         `json:"sortNo"`
}

type Condition struct {
	ExpectedValue string   `json:"expectedValue"`
	FieldName     string   `json:"fieldName"`
	Operator      Operator `json:"operator"`
	Source        Source   `json:"source"`
}

type Input struct {
	Body        []byte
	ContentType string
	Query       map[string][]string
}

type Decision struct {
	Result   Result
	RuleName string
}

// ResolveValues exposes the same query/body field resolution used by request
// validation to other request-routing policies. Keeping this narrow wrapper
// prevents domain routing from growing a second, subtly different JSON/form
// parser.
func ResolveValues(input Input, source Source, field string) ([]string, bool) {
	return newValueResolver(input).values(source, strings.TrimSpace(field))
}

// ExtractDomain exposes the shared URL/embedded-domain parser for policies
// that need to make a routing decision before an upstream request is sent.
func ExtractDomain(value string) (string, bool) {
	return extractDomain(value)
}

// NormalizeDomain exposes the canonical DNS-domain normalization used by
// DOMAIN_IN request-validation conditions.
func NormalizeDomain(value string) (string, error) {
	return normalizeDomain(value)
}

var embeddedHTTPURLPattern = regexp.MustCompile(`(?i)https?://[^\s<>"']+`)

var embeddedDomainPattern = regexp.MustCompile(`(?i)(?:https?://)?(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+(?:[a-z]{2,63}|xn--[a-z0-9-]{2,59})(?::[0-9]{1,5})?`)

var operatorsRequiringExpectedValue = map[Operator]bool{
	OperatorContains:    true,
	OperatorDomainIn:    true,
	OperatorDomainNotIn: true,
	OperatorEndsWith:    true,
	OperatorEqual:       true,
	OperatorNotContains: true,
	OperatorNotEqual:    true,
	OperatorNotRegex:    true,
	OperatorRegex:       true,
	OperatorStartsWith:  true,
}

var validOperators = map[Operator]bool{
	OperatorContains:     true,
	OperatorDomainIn:     true,
	OperatorDomainNotIn:  true,
	OperatorEmpty:        true,
	OperatorEndsWith:     true,
	OperatorEqual:        true,
	OperatorExists:       true,
	OperatorHasDomain:    true,
	OperatorIsURL:        true,
	OperatorNotContains:  true,
	OperatorNotEmpty:     true,
	OperatorNotEqual:     true,
	OperatorNotExists:    true,
	OperatorNotHasDomain: true,
	OperatorNotRegex:     true,
	OperatorNotURL:       true,
	OperatorRegex:        true,
	OperatorStartsWith:   true,
}

func Normalize(policy Policy) (Policy, error) {
	if policy.Enabled != 0 && policy.Enabled != 1 {
		return Policy{}, fmt.Errorf("enabled must be 0 or 1")
	}
	defaultResult, err := normalizeResult(policy.DefaultResult, ResultPass)
	if err != nil {
		return Policy{}, fmt.Errorf("default result: %w", err)
	}
	if len(policy.Rules) > MaxRules {
		return Policy{}, fmt.Errorf("rules cannot exceed %d", MaxRules)
	}

	normalized := Policy{
		DefaultResult: defaultResult,
		Enabled:       policy.Enabled,
		Rules:         make([]Rule, 0, len(policy.Rules)),
	}
	for index, rawRule := range policy.Rules {
		rule, err := normalizeRule(rawRule, index)
		if err != nil {
			return Policy{}, fmt.Errorf("rule %d: %w", index+1, err)
		}
		normalized.Rules = append(normalized.Rules, rule)
	}
	return normalized, nil
}

func Evaluate(policy Policy, input Input) Decision {
	if policy.Enabled != 1 {
		return Decision{Result: ResultPass}
	}
	resolver := newValueResolver(input)
	rules := append([]Rule(nil), policy.Rules...)
	sort.SliceStable(rules, func(i, j int) bool {
		left := rules[i].SortNo
		right := rules[j].SortNo
		if left <= 0 {
			left = i + 1
		}
		if right <= 0 {
			right = j + 1
		}
		return left < right
	})
	for _, rule := range rules {
		if ruleMatches(rule, resolver) {
			return Decision{Result: rule.Result, RuleName: rule.Name}
		}
	}
	result := policy.DefaultResult
	if !validResult(result) {
		result = ResultPass
	}
	return Decision{Result: result}
}

func normalizeRule(rule Rule, index int) (Rule, error) {
	rule.Name = strings.TrimSpace(rule.Name)
	if utf8.RuneCountInString(rule.Name) > MaxNameRunes {
		return Rule{}, fmt.Errorf("name cannot exceed %d characters", MaxNameRunes)
	}
	rule.Logic = Logic(strings.ToUpper(strings.TrimSpace(string(rule.Logic))))
	if rule.Logic == "" {
		rule.Logic = LogicAll
	}
	if rule.Logic != LogicAll && rule.Logic != LogicAny {
		return Rule{}, fmt.Errorf("logic must be ALL or ANY")
	}
	result, err := normalizeResult(rule.Result, "")
	if err != nil {
		return Rule{}, err
	}
	rule.Result = result
	if len(rule.Conditions) == 0 || len(rule.Conditions) > MaxConditionsPerRule {
		return Rule{}, fmt.Errorf("conditions must contain 1 to %d items", MaxConditionsPerRule)
	}
	rule.SortNo = index + 1
	normalizedConditions := make([]Condition, 0, len(rule.Conditions))
	for conditionIndex, rawCondition := range rule.Conditions {
		condition, err := normalizeCondition(rawCondition)
		if err != nil {
			return Rule{}, fmt.Errorf("condition %d: %w", conditionIndex+1, err)
		}
		normalizedConditions = append(normalizedConditions, condition)
	}
	rule.Conditions = normalizedConditions
	return rule, nil
}

func normalizeCondition(condition Condition) (Condition, error) {
	condition.Source = Source(strings.ToUpper(strings.TrimSpace(string(condition.Source))))
	if condition.Source == "" {
		condition.Source = SourceAuto
	}
	if condition.Source != SourceAuto && condition.Source != SourceBody && condition.Source != SourceQuery {
		return Condition{}, fmt.Errorf("source must be AUTO, QUERY, or BODY")
	}
	condition.FieldName = strings.TrimSpace(condition.FieldName)
	if condition.FieldName == "" || utf8.RuneCountInString(condition.FieldName) > MaxFieldRunes {
		return Condition{}, fmt.Errorf("field name is required and cannot exceed %d characters", MaxFieldRunes)
	}
	condition.Operator = Operator(strings.ToUpper(strings.TrimSpace(string(condition.Operator))))
	if !validOperators[condition.Operator] {
		return Condition{}, fmt.Errorf("unsupported operator %q", condition.Operator)
	}
	condition.ExpectedValue = strings.TrimSpace(condition.ExpectedValue)
	if utf8.RuneCountInString(condition.ExpectedValue) > MaxExpectedRunes {
		return Condition{}, fmt.Errorf("expected value cannot exceed %d characters", MaxExpectedRunes)
	}
	if operatorsRequiringExpectedValue[condition.Operator] && condition.ExpectedValue == "" {
		return Condition{}, fmt.Errorf("expected value is required for %s", condition.Operator)
	}
	switch condition.Operator {
	case OperatorDomainIn, OperatorDomainNotIn:
		normalized, err := normalizeDomainList(condition.ExpectedValue)
		if err != nil {
			return Condition{}, err
		}
		condition.ExpectedValue = normalized
	case OperatorRegex, OperatorNotRegex:
		if utf8.RuneCountInString(condition.ExpectedValue) > MaxRegexRunes {
			return Condition{}, fmt.Errorf("regular expression cannot exceed %d characters", MaxRegexRunes)
		}
		if _, err := regexp.Compile(condition.ExpectedValue); err != nil {
			return Condition{}, fmt.Errorf("invalid regular expression: %w", err)
		}
	}
	return condition, nil
}

func normalizeResult(value Result, fallback Result) (Result, error) {
	result := Result(strings.ToUpper(strings.TrimSpace(string(value))))
	if result == "" {
		result = fallback
	}
	if !validResult(result) {
		return "", fmt.Errorf("result must be PASS, INVALID_PARAMS, or PLATFORM_MISMATCH")
	}
	return result, nil
}

func validResult(result Result) bool {
	return result == ResultPass || result == ResultInvalidParams || result == ResultPlatformMismatch
}

type valueResolver struct {
	bodyForm url.Values
	bodyJSON any
	query    map[string][]string
}

func newValueResolver(input Input) *valueResolver {
	resolver := &valueResolver{query: input.Query}
	if len(input.Body) == 0 {
		return resolver
	}
	if decoded, err := jsonrequest.DecodeUnambiguous(input.Body, maxJSONDepth); err == nil {
		resolver.bodyJSON = decoded
	}
	mediaType := strings.ToLower(strings.TrimSpace(strings.SplitN(input.ContentType, ";", 2)[0]))
	if resolver.bodyJSON == nil || mediaType == "application/x-www-form-urlencoded" {
		if values, err := url.ParseQuery(string(input.Body)); err == nil {
			resolver.bodyForm = values
		}
	}
	return resolver
}

func (r *valueResolver) values(source Source, field string) ([]string, bool) {
	switch source {
	case SourceQuery:
		return queryValues(r.query, field)
	case SourceBody:
		return r.bodyValues(field)
	default:
		if values, ok := queryValues(r.query, field); ok {
			return values, true
		}
		return r.bodyValues(field)
	}
}

func queryValues(query map[string][]string, field string) ([]string, bool) {
	if query == nil {
		return nil, false
	}
	values, ok := query[field]
	return values, ok
}

func (r *valueResolver) bodyValues(field string) ([]string, bool) {
	if r.bodyJSON != nil {
		if value, ok := lookupValue(r.bodyJSON, field); ok {
			return stringValues(value), true
		}
	}
	if r.bodyForm != nil {
		values, ok := r.bodyForm[field]
		return values, ok
	}
	return nil, false
}

func lookupValue(source any, path string) (any, bool) {
	if path == "$" {
		return source, true
	}
	current := source
	for _, segment := range strings.Split(path, ".") {
		if segment == "" {
			return nil, false
		}
		switch typed := current.(type) {
		case map[string]any:
			value, ok := typed[segment]
			if !ok {
				return nil, false
			}
			current = value
		case []any:
			index, err := strconv.Atoi(segment)
			if err != nil || index < 0 || index >= len(typed) {
				return nil, false
			}
			current = typed[index]
		default:
			return nil, false
		}
	}
	return current, true
}

func stringValues(value any) []string {
	switch typed := value.(type) {
	case nil:
		return []string{""}
	case []any:
		values := make([]string, 0, len(typed))
		for _, item := range typed {
			values = append(values, stringValues(item)...)
		}
		return values
	case []string:
		return append([]string(nil), typed...)
	case string:
		return []string{typed}
	default:
		if data, err := json.Marshal(typed); err == nil {
			return []string{string(data)}
		}
		return []string{fmt.Sprintf("%v", typed)}
	}
}

func ruleMatches(rule Rule, resolver *valueResolver) bool {
	if len(rule.Conditions) == 0 {
		return false
	}
	if rule.Logic == LogicAny {
		for _, condition := range rule.Conditions {
			if conditionMatches(condition, resolver) {
				return true
			}
		}
		return false
	}
	for _, condition := range rule.Conditions {
		if !conditionMatches(condition, resolver) {
			return false
		}
	}
	return true
}

func conditionMatches(condition Condition, resolver *valueResolver) bool {
	values, found := resolver.values(condition.Source, condition.FieldName)
	// AUTO validation covers both supplied values: forwarding templates may use
	// the body even when the query has the same field. Routing's ResolveValues
	// keeps its documented query-first precedence; explicit sources stay scoped.
	if condition.Source == SourceAuto {
		query, queryFound := queryValues(resolver.query, condition.FieldName)
		body, bodyFound := resolver.bodyValues(condition.FieldName)
		if queryFound && bodyFound {
			values = append(append([]string(nil), query...), body...)
			if len(query) == 0 || len(body) == 0 {
				values = append(values, "")
			}
		}
	}
	if condition.Operator == OperatorNotExists {
		return !found
	}
	if !found {
		return false
	}
	if condition.Operator == OperatorExists {
		return true
	}
	expected := condition.ExpectedValue
	switch condition.Operator {
	case OperatorEmpty:
		return len(values) == 0 || anyValue(values, func(value string) bool { return strings.TrimSpace(value) == "" })
	case OperatorNotEmpty:
		return allValues(values, func(value string) bool { return strings.TrimSpace(value) != "" })
	case OperatorEqual:
		return allValues(values, func(value string) bool { return value == expected })
	case OperatorNotEqual:
		return anyValue(values, func(value string) bool { return value != expected })
	case OperatorContains:
		return allValues(values, func(value string) bool { return strings.Contains(value, expected) })
	case OperatorNotContains:
		return anyValue(values, func(value string) bool { return !strings.Contains(value, expected) })
	case OperatorStartsWith:
		return allValues(values, func(value string) bool { return strings.HasPrefix(value, expected) })
	case OperatorEndsWith:
		return allValues(values, func(value string) bool { return strings.HasSuffix(value, expected) })
	case OperatorRegex, OperatorNotRegex:
		pattern, err := regexp.Compile(expected)
		if err != nil {
			return false
		}
		if condition.Operator == OperatorRegex {
			return allValues(values, pattern.MatchString)
		}
		return anyValue(values, func(value string) bool { return !pattern.MatchString(value) })
	case OperatorIsURL:
		return allValues(values, isAbsoluteHTTPURL)
	case OperatorNotURL:
		return anyValue(values, func(value string) bool { return !isAbsoluteHTTPURL(value) })
	case OperatorHasDomain:
		return allValues(values, func(value string) bool { _, ok := extractDomain(value); return ok })
	case OperatorNotHasDomain:
		return anyValue(values, func(value string) bool { _, ok := extractDomain(value); return !ok })
	case OperatorDomainIn, OperatorDomainNotIn:
		allowed := strings.Split(expected, ",")
		matches := func(value string) (bool, bool) {
			host, ok := extractDomain(value)
			if !ok {
				return false, false
			}
			return domainAllowed(host, allowed), true
		}
		if condition.Operator == OperatorDomainIn {
			return allValues(values, func(value string) bool {
				matched, hasDomain := matches(value)
				return hasDomain && matched
			})
		}
		return anyValue(values, func(value string) bool {
			matched, hasDomain := matches(value)
			return hasDomain && !matched
		})
	default:
		return false
	}
}

func allValues(values []string, predicate func(string) bool) bool {
	if len(values) == 0 {
		return false
	}
	for _, value := range values {
		if !predicate(value) {
			return false
		}
	}
	return true
}

func anyValue(values []string, predicate func(string) bool) bool {
	for _, value := range values {
		if predicate(value) {
			return true
		}
	}
	return false
}

func isAbsoluteHTTPURL(value string) bool {
	raw := strings.TrimSpace(value)
	if raw == "" || strings.ContainsAny(raw, " \t\r\n") {
		return false
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.User != nil || parsed.Hostname() == "" {
		return false
	}
	return strings.EqualFold(parsed.Scheme, "http") || strings.EqualFold(parsed.Scheme, "https")
}

func extractDomain(value string) (string, bool) {
	raw := strings.TrimSpace(value)
	if raw == "" {
		return "", false
	}
	// Parse the complete link inside share text, not a domain-shaped substring
	// from its path. This also keeps @user paths distinct from URL credentials.
	if link := embeddedHTTPURLPattern.FindString(raw); link != "" {
		raw = link
	}
	parsed, parseErr := url.Parse(raw)
	if hasURLUserInfo(raw, parsed) {
		return "", false
	}
	if parseErr == nil && parsed.Host != "" {
		// A parsed URL's authority is authoritative. Never treat its path or
		// query as a fallback hostname when the actual host is invalid.
		host, err := normalizeDomain(parsed.Hostname())
		return host, err == nil
	}
	if parseErr != nil || parsed.IsAbs() {
		return "", false
	}
	candidate := embeddedDomainPattern.FindString(raw)
	if candidate == "" {
		return "", false
	}
	host, err := normalizeDomain(candidate)
	return host, err == nil
}

func normalizeDomainList(value string) (string, error) {
	rawDomains := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == '\r' || r == '\t' || r == ' '
	})
	if len(rawDomains) == 0 || len(rawDomains) > maxPlatformDomains {
		return "", fmt.Errorf("platform domains must contain 1 to %d entries", maxPlatformDomains)
	}
	seen := make(map[string]bool, len(rawDomains))
	normalized := make([]string, 0, len(rawDomains))
	for _, rawDomain := range rawDomains {
		domain, err := normalizeDomain(rawDomain)
		if err != nil {
			return "", fmt.Errorf("invalid platform domain %q: %w", rawDomain, err)
		}
		if !seen[domain] {
			seen[domain] = true
			normalized = append(normalized, domain)
		}
	}
	return strings.Join(normalized, ","), nil
}

func normalizeDomain(value string) (string, error) {
	raw := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(value), "*."))
	if raw == "" {
		return "", fmt.Errorf("domain is empty")
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" || parsed.User != nil {
		return "", fmt.Errorf("domain cannot be parsed")
	}
	host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	if net.ParseIP(host) != nil || len(host) > 253 || !strings.Contains(host, ".") {
		return "", fmt.Errorf("a DNS domain is required")
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", fmt.Errorf("domain label is invalid")
		}
		for _, char := range label {
			if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '-' {
				return "", fmt.Errorf("domain label contains unsupported characters")
			}
		}
	}
	return host, nil
}

// hasURLUserInfo rejects credentials before the embedded-domain fallback can
// extract only the host portion from an otherwise unsafe value. url.Parse
// handles normal absolute URLs; the second check covers credential-like bare
// values such as user:password@example.com/path.
func hasURLUserInfo(raw string, parsed *url.URL) bool {
	if parsed != nil && parsed.Host != "" {
		return parsed.User != nil
	}
	if at := strings.IndexByte(raw, '@'); at > 0 && strings.Contains(raw[:at], ":") {
		return embeddedDomainPattern.MatchString(raw[at+1:])
	}
	return false
}

func domainAllowed(host string, allowed []string) bool {
	for _, candidate := range allowed {
		candidate = strings.TrimSpace(strings.ToLower(candidate))
		if candidate != "" && (host == candidate || strings.HasSuffix(host, "."+candidate)) {
			return true
		}
	}
	return false
}
