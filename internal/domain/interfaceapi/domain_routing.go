package interfaceapi

import (
	"strings"
	"unicode/utf8"

	"bugpk-api/internal/pkg/domainrouting"
	apperrors "bugpk-api/internal/pkg/errors"
	"bugpk-api/internal/pkg/jsoncleaning"
	"bugpk-api/internal/pkg/requestvalidation"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

const maxDomainRoutingUpstreams = 200

type domainRouteSelection struct {
	Fallback      string
	MatchedDomain string
	RouteID       string
	RouteName     string
}

// resolveInterfaceDomainRoute applies the same request-domain decision used by
// the public gateway to the management test-invoke path. The returned item is
// a value copy whose upstream policy is replaced by the selected route; the
// interface-level identity, billing fields and request-validation policy stay
// untouched.
func resolveInterfaceDomainRoute(item InterfaceItem, req InvokePayload) (InterfaceItem, domainRouteSelection, error) {
	routing := item.DomainRouting
	if routing.Enabled != 1 {
		return item, domainRouteSelection{}, nil
	}
	fallback := strings.ToUpper(strings.TrimSpace(routing.Fallback))
	if fallback == "" {
		fallback = domainrouting.FallbackDefaultPool
	}
	if fallback != domainrouting.FallbackDefaultPool && fallback != domainrouting.FallbackDefaultRoute && fallback != domainrouting.FallbackReject {
		return InterfaceItem{}, domainRouteSelection{}, apperrors.New(consts.StatusServiceUnavailable, "域名子轮询配置暂时不可用")
	}
	routes, err := normalizedRuntimeDomainRoutes(routing.Routes)
	if err != nil {
		return InterfaceItem{}, domainRouteSelection{}, apperrors.New(consts.StatusServiceUnavailable, "域名子轮询配置暂时不可用")
	}
	rules := make([]domainrouting.Rule, 0, len(routes))
	for _, route := range routes {
		rules = append(rules, domainrouting.Rule{ID: route.ID, Name: route.Name, Enabled: route.Enabled, Priority: domainRoutePriority(route), PrioritySet: route.prioritySet || route.Priority != 0, Match: route.Match})
	}
	selection, matched := domainrouting.Select(rules, invokeDomainRoutingInput(req))
	if matched {
		if selection.RuleIndex < 0 || selection.RuleIndex >= len(routes) {
			return InterfaceItem{}, domainRouteSelection{}, apperrors.New(consts.StatusServiceUnavailable, "域名子轮询配置暂时不可用")
		}
		route := routes[selection.RuleIndex]
		return applyInterfaceDomainRoute(item, route), domainRouteSelection{
			Fallback:      fallback,
			MatchedDomain: selection.MatchedDomain,
			RouteID:       route.ID,
			RouteName:     route.Name,
		}, nil
	}
	switch fallback {
	case domainrouting.FallbackDefaultPool:
		return item, domainRouteSelection{Fallback: fallback}, nil
	case domainrouting.FallbackDefaultRoute:
		defaultID := strings.TrimSpace(routing.DefaultRouteID)
		for _, route := range routes {
			if route.ID == defaultID && route.Enabled == 1 {
				return applyInterfaceDomainRoute(item, route), domainRouteSelection{Fallback: fallback, RouteID: route.ID, RouteName: route.Name}, nil
			}
		}
		return InterfaceItem{}, domainRouteSelection{}, apperrors.New(consts.StatusServiceUnavailable, "默认域名子轮询路由暂时不可用")
	case domainrouting.FallbackReject:
		return InterfaceItem{}, domainRouteSelection{}, apperrors.NewWithData(consts.StatusUnprocessableEntity, "请求域名未匹配可用子轮询接口", map[string]any{
			"tag":            "DOMAIN_ROUTE_MISMATCH",
			"resourceAccess": "DENIED",
			"fallback":       fallback,
		})
	default:
		return InterfaceItem{}, domainRouteSelection{}, apperrors.New(consts.StatusServiceUnavailable, "域名子轮询回退策略暂时不可用")
	}
}

// normalizedRuntimeDomainRoutes normalizes only the route metadata needed by
// runtime matching. It deliberately preserves health telemetry on upstream
// rows, unlike the save-time normalizer which clears that telemetry.
func normalizedRuntimeDomainRoutes(input []DomainPollingRoute) ([]DomainPollingRoute, error) {
	metadata := make([]domainrouting.Rule, 0, len(input))
	for _, route := range input {
		metadata = append(metadata, domainrouting.Rule{ID: route.ID, Name: route.Name, Enabled: route.Enabled, Priority: domainRoutePriority(route), PrioritySet: route.prioritySet || route.Priority != 0, Match: route.Match})
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

func invokeDomainRoutingInput(req InvokePayload) requestvalidation.Input {
	input := invokeValidationInput(req)
	// The admin invoke form accepts a raw JSON string as well as an object. The
	// normal validation helper marshals strings as JSON string literals; route
	// matching needs the original document so BODY payload.url and form fields
	// resolve exactly like an open request.
	if raw, ok := req.Body.(string); ok {
		if strings.TrimSpace(raw) == "" {
			input.Body = nil
		} else {
			input.Body = []byte(raw)
		}
	}
	return input
}

func applyInterfaceDomainRoute(item InterfaceItem, route DomainPollingRoute) InterfaceItem {
	item.domainRouteID = strings.TrimSpace(route.ID)
	item.RequestURL = strings.TrimSpace(route.RequestURL)
	item.PollingEnabled = route.PollingEnabled
	item.PollingMode = strings.TrimSpace(route.PollingMode)
	item.PollingCheckEnabled = route.PollingCheckEnabled
	item.PollingCheckField = route.PollingCheckField
	item.PollingCheckExpected = route.PollingCheckExpected
	item.PollingStopCode = route.PollingStopCode
	item.ResponseCleaningEnabled = route.ResponseCleaningEnabled
	item.ResponseFieldMappings = append([]jsoncleaning.FieldMapping(nil), route.ResponseFieldMappings...)
	item.UpstreamConfigs = cloneInterfaceDomainUpstreams(route.UpstreamConfigs)
	item.UpstreamURLs = upstreamURLs(item.UpstreamConfigs)
	item.UpstreamCount = displayUpstreamCount(item)
	// Route-level legacy inference needs the copied pool and stop code. Doing
	// this before those fields are present collapses an empty historical mode
	// to SINGLE and silently suppresses its backups. Explicit SINGLE stays off.
	item.PollingMode = normalizedPollingMode(item.PollingMode, boolInt(pollingRuntimeEnabled(item)))
	return item
}

func cloneInterfaceDomainUpstreams(input []UpstreamConfig) []UpstreamConfig {
	if len(input) == 0 {
		return nil
	}
	result := make([]UpstreamConfig, len(input))
	copy(result, input)
	for index := range result {
		result[index].ResponseFieldMappings = append([]jsoncleaning.FieldMapping(nil), result[index].ResponseFieldMappings...)
	}
	return result
}

func normalizeDomainRouting(input *DomainRoutingConfig) (DomainRoutingConfig, error) {
	if input == nil {
		return DomainRoutingConfig{}, nil
	}
	if input.Enabled != 0 && input.Enabled != 1 {
		return DomainRoutingConfig{}, apperrors.New(consts.StatusBadRequest, "域名子轮询启用状态不合法")
	}
	fallback := strings.ToUpper(strings.TrimSpace(input.Fallback))
	if fallback == "" {
		fallback = domainrouting.FallbackDefaultPool
	}
	if fallback != domainrouting.FallbackDefaultPool && fallback != domainrouting.FallbackDefaultRoute && fallback != domainrouting.FallbackReject {
		return DomainRoutingConfig{}, apperrors.New(consts.StatusBadRequest, "域名子轮询回退策略不合法")
	}
	defaultRouteID := strings.TrimSpace(input.DefaultRouteID)
	if utf8.RuneCountInString(defaultRouteID) > 64 {
		return DomainRoutingConfig{}, apperrors.New(consts.StatusBadRequest, "域名子轮询默认路由编号过长")
	}
	metadata := make([]domainrouting.Rule, 0, len(input.Routes))
	for _, route := range input.Routes {
		metadata = append(metadata, domainrouting.Rule{
			ID: route.ID, Name: route.Name, Enabled: route.Enabled, Priority: domainRoutePriority(route), PrioritySet: route.prioritySet || route.Priority != 0, Match: route.Match,
		})
	}
	normalizedMetadata, err := domainrouting.NormalizeRules(metadata)
	if err != nil {
		return DomainRoutingConfig{}, apperrors.New(consts.StatusBadRequest, "域名子轮询匹配规则不合法")
	}
	routes := make([]DomainPollingRoute, 0, len(input.Routes))
	totalUpstreams := 0
	for index, source := range input.Routes {
		route := source
		route.ID = normalizedMetadata[index].ID
		route.Name = normalizedMetadata[index].Name
		route.Enabled = normalizedMetadata[index].Enabled
		route.Priority = normalizedMetadata[index].Priority
		route.Match = normalizedMetadata[index].Match
		route.RequestURL = strings.TrimSpace(route.RequestURL)
		mode, modeErr := normalizePollingMode(route.PollingMode, route.PollingEnabled)
		if route.PollingEnabled != 0 && route.PollingEnabled != 1 {
			return DomainRoutingConfig{}, apperrors.New(consts.StatusBadRequest, "域名子轮询路由状态值不合法")
		}
		if modeErr != nil {
			return DomainRoutingConfig{}, modeErr
		}
		// As with the interface-level switch, store an explicitly disabled
		// route as SINGLE. This preserves a real operator opt-out while runtime
		// compatibility can still recognize old zero-bit/non-SINGLE documents.
		if route.PollingEnabled == 0 {
			mode = "SINGLE"
		}
		if route.RequestURL != "" {
			if err := validateHTTPURL(route.RequestURL, 1024, "域名子轮询路由接口地址"); err != nil {
				return DomainRoutingConfig{}, err
			}
		}
		if mode == "SINGLE" || mode == "PRIMARY" || mode == "FASTEST" {
			if route.RequestURL == "" {
				return DomainRoutingConfig{}, apperrors.New(consts.StatusBadRequest, "SINGLE、PRIMARY或FASTEST路由必须填写主接口地址")
			}
		}
		if route.ResponseCleaningEnabled != 0 && route.ResponseCleaningEnabled != 1 {
			return DomainRoutingConfig{}, apperrors.New(consts.StatusBadRequest, "域名子轮询响应清洗状态值不合法")
		}
		if utf8.RuneCountInString(strings.TrimSpace(route.PollingStopCode)) > 64 {
			return DomainRoutingConfig{}, apperrors.New(consts.StatusBadRequest, "域名子轮询终止响应码过长")
		}
		route.PollingCheckField = defaultText(strings.TrimSpace(route.PollingCheckField), "code")
		route.PollingCheckExpected = defaultText(strings.TrimSpace(route.PollingCheckExpected), "200")
		if utf8.RuneCountInString(route.PollingCheckField) > 128 || utf8.RuneCountInString(route.PollingCheckExpected) > 255 {
			return DomainRoutingConfig{}, apperrors.New(consts.StatusBadRequest, "域名子轮询响应校验字段或期望值过长")
		}
		route.PollingStopCode = strings.TrimSpace(route.PollingStopCode)
		route.ResponseFieldMappings, err = normalizeResponseFieldMappings(route.ResponseFieldMappings)
		if err != nil {
			return DomainRoutingConfig{}, err
		}
		configs := route.UpstreamConfigs
		seen := make(map[string]struct{}, len(configs))
		normalizedConfigs := make([]UpstreamConfig, 0, len(configs))
		for _, config := range configs {
			urlText := strings.TrimSpace(config.URL)
			if urlText == "" {
				continue
			}
			if _, exists := seen[urlText]; exists {
				continue
			}
			if err := validateHTTPURL(urlText, 1024, "域名子轮询上游接口地址"); err != nil {
				return DomainRoutingConfig{}, err
			}
			config.URL = urlText
			config.PollingCheckField = defaultText(strings.TrimSpace(config.PollingCheckField), "code")
			config.PollingCheckExpected = defaultText(strings.TrimSpace(config.PollingCheckExpected), "200")
			if config.PollingCheckEnabled != 0 && config.PollingCheckEnabled != 1 {
				return DomainRoutingConfig{}, apperrors.New(consts.StatusBadRequest, "域名子轮询上游响应校验状态不合法")
			}
			if utf8.RuneCountInString(config.PollingCheckField) > 128 || utf8.RuneCountInString(config.PollingCheckExpected) > 255 {
				return DomainRoutingConfig{}, apperrors.New(consts.StatusBadRequest, "域名子轮询上游响应校验字段或期望值过长")
			}
			config.ResponseFieldMappings, err = normalizeResponseFieldMappings(config.ResponseFieldMappings)
			if err != nil {
				return DomainRoutingConfig{}, apperrors.Wrap(consts.StatusBadRequest, "域名子轮询上游响应字段映射不合法", err)
			}
			config.HealthStatus = ""
			config.LastCheckedAt = ""
			config.LastLatencyMs = 0
			config.FailureCount = 0
			seen[urlText] = struct{}{}
			normalizedConfigs = append(normalizedConfigs, config)
		}
		if len(normalizedConfigs) > 50 {
			return DomainRoutingConfig{}, apperrors.New(consts.StatusBadRequest, "单个域名子轮询路由上游节点不能超过50个")
		}
		if route.RequestURL == "" && len(normalizedConfigs) == 0 {
			return DomainRoutingConfig{}, apperrors.New(consts.StatusBadRequest, "域名子轮询路由至少需要一个主接口或上游节点")
		}
		route.PollingMode = mode
		route.UpstreamConfigs = normalizedConfigs
		totalUpstreams += len(normalizedConfigs)
		routes = append(routes, route)
	}
	if totalUpstreams > maxDomainRoutingUpstreams {
		return DomainRoutingConfig{}, apperrors.New(consts.StatusBadRequest, "所有域名子轮询路由上游节点不能超过200个")
	}
	if defaultRouteID != "" {
		found := false
		for _, route := range routes {
			if route.ID == defaultRouteID {
				if route.Enabled != 1 {
					return DomainRoutingConfig{}, apperrors.New(consts.StatusBadRequest, "默认域名子轮询路由必须处于启用状态")
				}
				found = true
				break
			}
		}
		if !found {
			return DomainRoutingConfig{}, apperrors.New(consts.StatusBadRequest, "默认域名子轮询路由不存在")
		}
	}
	if fallback == domainrouting.FallbackDefaultRoute && defaultRouteID == "" {
		return DomainRoutingConfig{}, apperrors.New(consts.StatusBadRequest, "DEFAULT_ROUTE必须指定默认路由")
	}
	return DomainRoutingConfig{Enabled: input.Enabled, Fallback: fallback, DefaultRouteID: defaultRouteID, Routes: routes}, nil
}

func domainRoutePriority(route DomainPollingRoute) int {
	if route.Priority == 0 && !route.prioritySet {
		return domainrouting.DefaultPriority
	}
	return route.Priority
}
