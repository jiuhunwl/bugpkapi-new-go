package observability

import (
	"context"

	"bugpk-api/internal/domain/auth"
)

// AuthAuditAdapter 把认证域的审计投递转换成 observability 的记录写入。
//
// 存在的意义是解耦方向：auth 是最底层的基础设施域，如果它直接 import
// observability，就会形成「基础设施依赖业务模块」的反向依赖，并且让
// auth 的单测被迫拉起整条 observability 依赖链。适配器只做字段搬运。
type AuthAuditAdapter struct {
	service *Service
}

func NewAuthAuditAdapter(service *Service) *AuthAuditAdapter {
	return &AuthAuditAdapter{service: service}
}

// RecordAuth 实现 auth.AuditRecorder。
func (a *AuthAuditAdapter) RecordAuth(ctx context.Context, entry auth.AuditEntry) {
	if a == nil || a.service == nil || entry.UserID <= 0 || (entry.Action != "login" && entry.Action != "logout") {
		return
	}
	module := entry.Module
	if module == "" {
		module = string(OperationLogModuleAuth)
	}
	statusCode := entry.StatusCode
	if statusCode == 0 {
		if entry.Status {
			statusCode = 200
		} else {
			statusCode = 403
		}
	}
	rec := OperationLogRecord{
		UserID:       entry.UserID,
		ActorKind:    operationActorKind(entry.Roles),
		Username:     entry.Username,
		Module:       module,
		Action:       entry.Action,
		Summary:      safeAuthSummary(entry.Action, entry.Summary, entry.Status),
		Method:       entry.Method,
		Path:         entry.Path,
		Status:       entry.Status,
		ErrorMessage: "",
		StatusCode:   statusCode,
		ClientIP:     entry.ClientIP,
		UserAgent:    "",
		ElapsedMS:    entry.ElapsedMS,
	}
	if state, ok := ctx.Value(requestAuditKey{}).(*requestAuditState); ok {
		state.authRecord = &rec
		return
	}
	// Non-HTTP callers still persist immediately.
	a.service.RecordOperation(ctx, rec)
}

// safeAuthSummary accepts only complete, fixed domain messages with a matching
// action/outcome. It never parses response bodies or forwards arbitrary errors.
// Recheck against the final HTTP outcome so a failed response cannot say success.
func safeAuthSummary(action, summary string, succeeded bool) string {
	switch action {
	case "login":
		if succeeded {
			switch summary {
			case "登录成功", "快捷登录成功":
				return summary
			}
			return "登录成功"
		}
		switch summary {
		case "登录失败：账号或密码错误", "登录被拒绝：账号已被禁用", "登录失败：账号已被禁用", "禁用账号":
			return summary
		}
		return "登录失败"
	case "logout":
		if succeeded {
			return "退出登录成功"
		}
		return "退出登录失败"
	default:
		if succeeded {
			return "认证成功"
		}
		return "认证失败"
	}
}
