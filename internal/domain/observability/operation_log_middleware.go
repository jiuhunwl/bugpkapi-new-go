package observability

import (
	"context"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"bugpk-api/internal/domain/rbac"
	"bugpk-api/internal/pkg/auditprincipal"

	hertzapp "github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

// requestAuditState only lives for a synchronous HTTP handler chain. The auth
// adapter enriches the final HTTP record instead of inserting a duplicate row.
type requestAuditKey struct{}
type requestAuditState struct{ authRecord *OperationLogRecord }

// OperationAuditMiddleware records attributed login/logout events and meaningful
// console operations only. Authentication is snapshotted BEFORE mutations (which
// may revoke tokens, delete users or change roles). Never inspect HTTP bodies.
func (s *Service) OperationAuditMiddleware(prefixes ...string) hertzapp.HandlerFunc {
	prefix := "/system-api"
	if len(prefixes) > 0 && prefixes[0] != "" {
		prefix = strings.TrimSuffix(prefixes[0], "/")
	}
	return func(ctx context.Context, c *hertzapp.RequestContext) {
		ctx = auditprincipal.WithRequest(rbac.WithRequestAuthentication(ctx), bearerTokenFromRequest(c))
		started := time.Now()
		method, path := string(c.Method()), c.FullPath()
		policyPath := ""
		if strings.HasPrefix(path, prefix+"/") {
			policyPath = "/system-api" + strings.TrimPrefix(path, prefix)
		}
		meaningful := meaningfulConsoleOperation(method, policyPath)
		var principal rbac.Principal
		var authenticated bool
		if meaningful {
			principal, authenticated = s.principalFromContext(ctx, c)
		}
		audit := &requestAuditState{}
		ctx = context.WithValue(ctx, requestAuditKey{}, audit)
		c.Next(ctx)
		if meaningful && !authenticated {
			if identity, ok := auditprincipal.FromContext(ctx); ok {
				principal = rbac.Principal{ID: identity.UserID, Username: identity.Username, Roles: identity.Roles}
				authenticated = true
			}
		}
		statusCode := c.Response.StatusCode()
		entry := audit.authRecord
		if entry == nil && (!meaningful || !authenticated || principal.ID <= 0) {
			return
		}
		if entry == nil && statusCode >= 300 && statusCode < 400 {
			return
		}
		module, action := classifyOperation(method, policyPath)
		rec := OperationLogRecord{
			UserID: principal.ID, Username: principal.Username, ActorKind: operationActorKind(principal.Roles),
			Module: module, Action: action, Method: method, Path: path,
			Status: statusCode >= 200 && statusCode < 300, StatusCode: statusCode,
			ClientIP: c.ClientIP(), ElapsedMS: int(time.Since(started).Milliseconds()),
		}
		// Known numeric resource parameters only; never serialize arbitrary params.
		// Keep the existing parent id priority for nested resource routes.
		for _, name := range []string{"id", "quickReplyId", "attachmentId"} {
			if target := c.Param(name); len(target) <= 64 && isNumericSegment(target) {
				rec.TargetID = target
				break
			}
		}
		if entry != nil {
			rec.UserID, rec.Username, rec.ActorKind = entry.UserID, entry.Username, entry.ActorKind
			rec.Module, rec.Action = entry.Module, entry.Action
			rec.Status = entry.Status && statusCode >= 200 && statusCode < 400
		}
		rec.Summary = operationSummary(method, path, rec.Status)
		if entry != nil {
			rec.Summary = safeAuthSummary(entry.Action, entry.Summary, rec.Status)
		}
		if !rec.Status {
			rec.ErrorMessage = "HTTP " + strconv.Itoa(statusCode)
		}
		s.RecordOperation(ctx, rec)
	}
}

// Explicit roots keep public endpoints out, even when a caller attaches a valid
// bearer. The exceptions below were checked against every route registration.
func meaningfulConsoleOperation(method, path string) bool {
	p := strings.TrimPrefix(path, "/system-api")
	if p == path {
		return false
	}
	if method == consts.MethodGet {
		return p == "/interface/call-log/export" || (strings.HasPrefix(p, "/key/") && strings.HasSuffix(p, "/secret"))
	}
	if !auditedMethod(method) {
		return false
	}
	if strings.HasPrefix(p, "/auth/") || p == "/friend-links/apply" || strings.HasPrefix(p, "/system/operation-log/") {
		return false
	}
	// Callback handlers and user order sync are public/background traffic, not
	// user-initiated console changes. Admin order sync can settle an order.
	for _, noise := range []string{"/payment/alipay/notify", "/payment/wechat/notify", "/payment/hupijiao/notify/"} {
		if p == noise || strings.HasPrefix(p, noise+"/") || (strings.HasSuffix(noise, "/") && strings.HasPrefix(p, noise)) {
			return false
		}
	}
	if method == consts.MethodPost {
		switch p {
		case "/payment/alipay/config/validate", "/payment/wechat/config/validate", "/payment/hupijiao/config/validate",
			"/interface/proxy-pool/inspect", "/interface/proxy-pool/test", "/system/ip-config/test-source",
			"/system/register/email-config/test", "/system/register/mobile-config/test",
			"/user/send-bind-email-code", "/user/send-bind-mobile-code":
			return false
		}
		if strings.HasPrefix(p, "/interface/") && (strings.HasSuffix(p, "/upstreams/check") || strings.HasSuffix(p, "/invoke")) {
			return false
		}
		if strings.HasPrefix(p, "/user/oauth-bindings/") && strings.HasSuffix(p, "/authorize") {
			return false
		}
		if strings.HasPrefix(p, "/tickets/") && strings.HasSuffix(p, "/read") {
			return false
		}
		if strings.HasPrefix(p, "/payment/recharge/orders/") && strings.HasSuffix(p, "/sync") {
			return false
		}
	}
	for _, prefix := range []string{"/system", "/user", "/menu", "/interface", "/key", "/package", "/payment", "/tickets", "/risk-control", "/upload", "/notice/admin", "/site/admin", "/donation/admin", "/friend-links/admin", "/advertisement/admin"} {
		if p == prefix || strings.HasPrefix(p, prefix+"/") {
			return true
		}
	}
	return false
}

func operationActorKind(roles []string) string {
	for _, role := range roles {
		if role == "admin" {
			return "admin"
		}
	}
	return "user"
}

// auditedMethod 判定该 HTTP 方法是否属于「改变状态」的操作。
func auditedMethod(method string) bool {
	switch method {
	case consts.MethodPost, consts.MethodPut, consts.MethodPatch, consts.MethodDelete:
		return true
	default:
		return false
	}
}

// Authentication is live for each HTTP request. The same request may reuse its
// credential-bound snapshot; no prior request supplies roles or token versions.
func (s *Service) principalFromContext(ctx context.Context, c *hertzapp.RequestContext) (rbac.Principal, bool) {
	if p, ok := rbac.PrincipalFromContext(ctx); ok {
		return p, p.ID > 0
	}
	if s.authz == nil || bearerTokenFromRequest(c) == "" {
		return rbac.Principal{}, false
	}
	p, err := s.authz.Authenticate(ctx, bearerTokenFromRequest(c))
	return p, err == nil && p.ID > 0
}

// bearerTokenFromRequest 从 Authorization 头提取 bearer 令牌。
func bearerTokenFromRequest(c *hertzapp.RequestContext) string {
	value := string(c.Request.Header.Peek("Authorization"))
	if len(value) > 7 && strings.EqualFold(value[:7], "bearer ") {
		return strings.TrimSpace(value[7:])
	}
	return ""
}

// managedPathPrefixToModule 把管理端路径前缀映射到审计模块码。
//
// 长前缀优先匹配（"risk-control" 必须先于 "risk"），因此用有序切片而非 map。
var managedPathPrefixToModule = []struct {
	prefix string
	module string
}{
	{"/system-api/auth", string(OperationLogModuleAuth)},
	// 开放 API 的专项前缀必须先于父前缀（user/system）匹配，否则会被归入父模块。
	{"/system-api/user/open-api", string(OperationLogModuleOpenAPI)},
	{"/system-api/user", string(OperationLogModuleUser)},
	{"/system-api/interface", string(OperationLogModuleInterface)},
	{"/system-api/key", string(OperationLogModuleKey)},
	{"/system-api/package", string(OperationLogModulePackage)},
	{"/system-api/payment", string(OperationLogModulePayment)},
	{"/system-api/content", string(OperationLogModuleContent)},
	{"/system-api/notice", string(OperationLogModuleContent)},
	{"/system-api/friend-links", string(OperationLogModuleContent)},
	{"/system-api/donation", string(OperationLogModuleContent)},
	{"/system-api/advertisement", string(OperationLogModuleContent)},
	{"/system-api/menu", string(OperationLogModulePermission)},
	{"/system-api/tickets", string(OperationLogModuleTicket)},
	{"/system-api/risk-control", string(OperationLogModuleRisk)},
	{"/system-api/redeem", string(OperationLogModuleRedeem)},
	{"/system-api/system/open-api", string(OperationLogModuleOpenAPI)},
	{"/system-api/system", string(OperationLogModuleSystem)},
	{"/system-api/permission", string(OperationLogModulePermission)},
	{"/system-api/rbac", string(OperationLogModulePermission)},
}

// classifyOperation 由方法 + 路径推断模块与动作。
//
// 推断而不是要求每个 handler 显式声明，是为了让新增接口自动获得审计覆盖——
// 显式声明一旦忘记就会留下审计盲区，而漏审计比审计冗余危险得多。
func classifyOperation(method, path string) (string, string) {
	module := string(OperationLogModuleSystem)
	for _, entry := range managedPathPrefixToModule {
		if path == entry.prefix || strings.HasPrefix(path, entry.prefix+"/") {
			module = entry.module
			break
		}
	}
	return module, actionFromRequest(method, path)
}

// actionFromRequest 依据方法与路径尾部推断动作码。
//
// 尾部为动词片段（如 /review、/status、/reset-password）时优先取该动词，
// 否则退回方法语义（POST=create、PUT/PATCH=update、DELETE=delete）。
func actionFromRequest(method, path string) string {
	tail := pathTail(path)
	switch tail {
	case "login":
		return string(OperationLogActionLogin)
	case "logout":
		return string(OperationLogActionLogout)
	case "register":
		return string(OperationLogActionRegister)
	case "password-reset":
		return string(OperationLogActionPassword)
	case "review", "approve", "reject":
		return string(OperationLogActionReview)
	case "status", "enable", "disable", "ban", "unban":
		return string(OperationLogActionStatus)
	case "secret":
		return "key-secret"
	case "export":
		return string(OperationLogActionExport)
	case "reset-password", "password", "change-password":
		return string(OperationLogActionPassword)
	case "config", "settings":
		return string(OperationLogActionConfig)
	case "adjust", "adjust-asset", "balance":
		return string(OperationLogActionAdjustAsset)
	}
	switch method {
	case consts.MethodPost:
		return string(OperationLogActionCreate)
	case consts.MethodPut, consts.MethodPatch:
		return string(OperationLogActionUpdate)
	case consts.MethodDelete:
		return string(OperationLogActionDelete)
	default:
		return strings.ToLower(method)
	}
}

// pathTail 取路径最后一个非数字、非空片段，忽略资源 ID。
func pathTail(path string) string {
	segments := strings.Split(strings.Trim(path, "/"), "/")
	for i := len(segments) - 1; i >= 0; i-- {
		segment := segments[i]
		if segment == "" || isNumericSegment(segment) {
			continue
		}
		return segment
	}
	return ""
}

func isNumericSegment(segment string) bool {
	for _, r := range segment {
		if r < '0' || r > '9' {
			return false
		}
	}
	return segment != ""
}

func operationSummary(method, path string, succeeded bool) string {
	result := "成功"
	if !succeeded {
		result = "失败"
	}
	return method + " " + path + " " + result
}

// truncateMessage 限制错误信息长度，避免把整个响应体写进审计表。
const maxAuditMessageLen = 500

func truncateMessage(body string) string {
	body = strings.TrimSpace(body)
	if len(body) <= maxAuditMessageLen {
		return body
	}
	end := maxAuditMessageLen
	for end > 0 && !utf8.RuneStart(body[end]) {
		end--
	}
	return body[:end]
}
