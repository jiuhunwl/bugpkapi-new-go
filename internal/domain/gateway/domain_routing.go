package gateway

import (
	"strings"

	"bugpk-api/internal/pkg/domainrouting"
	apperrors "bugpk-api/internal/pkg/errors"
	"bugpk-api/internal/pkg/jsoncleaning"
	"bugpk-api/internal/pkg/requestvalidation"

	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

type domainRouteSelection struct {
	Fallback      string
	MatchedDomain string
	RouteID       string
	RouteName     string
}

// resolveDomainRoute selects one effective upstream pool for an open request.
// It deliberately returns a copy of InterfaceConfig so interface-level billing,
// QPS and delivery policy remain unchanged while only the upstream policy is
// replaced.
func resolveDomainRoute(api InterfaceConfig, req OpenRequest) (InterfaceConfig, domainRouteSelection, error) {
	routing := api.DomainRouting
	if routing.Enabled != 1 {
		return api, domainRouteSelection{}, nil
	}
	fallback := strings.ToUpper(strings.TrimSpace(routing.Fallback))
	if fallback == "" {
		fallback = domainrouting.FallbackDefaultPool
	}
	if fallback != domainrouting.FallbackDefaultPool && fallback != domainrouting.FallbackDefaultRoute && fallback != domainrouting.FallbackReject {
		return InterfaceConfig{}, domainRouteSelection{}, apperrors.New(consts.StatusServiceUnavailable, "域名子轮询回退策略暂时不可用")
	}
	// Stored configs have already passed management validation. A malformed
	// historical document must fail closed rather than silently widening the
	// route match surface.
	routes, err := normalizedRuntimeDomainRoutes(routing.Routes)
	if err != nil {
		return InterfaceConfig{}, domainRouteSelection{}, apperrors.New(consts.StatusServiceUnavailable, "域名子轮询配置暂时不可用")
	}
	rules := make([]domainrouting.Rule, 0, len(routes))
	for _, route := range routes {
		rules = append(rules, domainrouting.Rule{
			ID: route.ID, Name: route.Name, Enabled: route.Enabled, Priority: route.Priority, PrioritySet: route.prioritySet, Match: route.Match,
		})
	}
	selection, matched := domainrouting.Select(rules, requestvalidation.Input{
		Body:        req.Body,
		ContentType: headerValue(req.Headers, "Content-Type"),
		Query:       req.Query,
	})
	if matched {
		if selection.RuleIndex < 0 || selection.RuleIndex >= len(routing.Routes) {
			return InterfaceConfig{}, domainRouteSelection{}, apperrors.New(consts.StatusServiceUnavailable, "域名子轮询配置暂时不可用")
		}
		route := routes[selection.RuleIndex]
		return applyDomainRoute(api, route), domainRouteSelection{
			Fallback:      fallback,
			MatchedDomain: selection.MatchedDomain,
			RouteID:       route.ID,
			RouteName:     route.Name,
		}, nil
	}
	switch fallback {
	case domainrouting.FallbackDefaultPool:
		return api, domainRouteSelection{Fallback: fallback}, nil
	case domainrouting.FallbackDefaultRoute:
		defaultID := strings.TrimSpace(routing.DefaultRouteID)
		for _, route := range routes {
			if route.ID == defaultID && route.Enabled == 1 {
				return applyDomainRoute(api, route), domainRouteSelection{Fallback: fallback, RouteID: route.ID, RouteName: route.Name}, nil
			}
		}
		return InterfaceConfig{}, domainRouteSelection{}, apperrors.New(consts.StatusServiceUnavailable, "默认域名子轮询路由暂时不可用")
	case domainrouting.FallbackReject:
		return InterfaceConfig{}, domainRouteSelection{}, apperrors.NewWithData(consts.StatusUnprocessableEntity, "请求域名未匹配可用子轮询接口", map[string]any{
			"tag":            "DOMAIN_ROUTE_MISMATCH",
			"resourceAccess": "DENIED",
			"fallback":       fallback,
		})
	default:
		return InterfaceConfig{}, domainRouteSelection{}, apperrors.New(consts.StatusServiceUnavailable, "域名子轮询回退策略暂时不可用")
	}
}

// normalizedRuntimeDomainRoutes applies the same identity and match-rule
// defaults used when a configuration is saved, while retaining the route's
// polling and health fields. Historical rows may omit an ID; using the raw
// route in that case would collapse its health and round-robin state into the
// default pool.
func normalizedRuntimeDomainRoutes(input []DomainPollingRoute) ([]DomainPollingRoute, error) {
	metadata := make([]domainrouting.Rule, 0, len(input))
	for _, route := range input {
		metadata = append(metadata, domainrouting.Rule{
			ID: route.ID, Name: route.Name, Enabled: route.Enabled, Priority: route.Priority, PrioritySet: route.prioritySet, Match: route.Match,
		})
	}
	normalized, err := domainrouting.NormalizeRules(metadata)
	if err != nil {
		return nil, err
	}
	routes := make([]DomainPollingRoute, len(input))
	for index, source := range input {
		route := source
		route.ID = normalized[index].ID
		route.Name = normalized[index].Name
		route.Enabled = normalized[index].Enabled
		route.Priority = normalized[index].Priority
		route.Match = normalized[index].Match
		routes[index] = route
	}
	return routes, nil
}

func applyDomainRoute(api InterfaceConfig, route DomainPollingRoute) InterfaceConfig {
	api.domainRouteID = strings.TrimSpace(route.ID)
	api.RequestURL = strings.TrimSpace(route.RequestURL)
	api.PollingEnabled = route.PollingEnabled
	api.PollingMode = strings.TrimSpace(route.PollingMode)
	api.PollingCheckEnabled = route.PollingCheckEnabled
	api.PollingCheckField = route.PollingCheckField
	api.PollingCheckExpected = route.PollingCheckExpected
	api.PollingStopCode = route.PollingStopCode
	api.ResponseCleaningEnabled = route.ResponseCleaningEnabled
	api.ResponseFieldMappings = cloneDomainMappings(route.ResponseFieldMappings)
	api.UpstreamConfigs = cloneDomainUpstreams(route.UpstreamConfigs)
	// Infer the runtime state only after the complete route pool and stop-code
	// contract have been copied. Historical route documents may have a zero
	// enable bit and an empty mode despite carrying polling intent; an explicit
	// SINGLE value remains the unambiguous operator opt-out.
	api.PollingMode = normalizedPollingMode(api.PollingMode, boolInt(pollingRuntimeEnabled(api)))
	return api
}

func cloneDomainMappings(input []jsoncleaning.FieldMapping) []jsoncleaning.FieldMapping {
	if len(input) == 0 {
		return nil
	}
	return append([]jsoncleaning.FieldMapping(nil), input...)
}

func cloneDomainUpstreams(input []UpstreamConfig) []UpstreamConfig {
	if len(input) == 0 {
		return nil
	}
	result := make([]UpstreamConfig, len(input))
	copy(result, input)
	for index := range result {
		result[index].ResponseFieldMappings = cloneDomainMappings(result[index].ResponseFieldMappings)
	}
	return result
}
