package accountstatus

import (
	"strings"

	apperrors "bugpk-api/internal/pkg/errors"

	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

const (
	Banned = 0
	Active = 1

	BanTag              = "ACCOUNT_BANNED"
	BanTypePermanent    = "PERMANENT"
	BanTypeTemporary    = "TEMPORARY"
	ResourceAccessDeny  = "DENIED"
	AccountStatusBanned = "BANNED"
)

// IsActive 是账号可用性的唯一 Go 侧判定入口。
//
// 账号能不能用产品只由这一个谓词决定，业务代码不得再自己写 status 比较。
// 三个接入平面各调用一次——JWT 服务器级守卫、/api 的 OpenKey、/open-api 的
// OpenAuthMiddleware——之后所有 handler 一律不再判断账号状态。这样封禁
// 只需要翻转 bp_user 一个开关，不存在「某功能忘了加判断」或「某功能按自己
// 缓存的标记判断」的第二套真相。
//
// SQL 侧对应 accountstatus.EffectiveStatusSQL，两者必须保持等价。
func IsActive(status int) bool {
	return status == Active
}

type BanDetails struct {
	Tag            string `json:"tag"`
	AccountStatus  string `json:"accountStatus"`
	BanType        string `json:"banType"`
	BanReason      string `json:"banReason"`
	BanUntil       string `json:"banUntil,omitempty"`
	Permanent      bool   `json:"permanent"`
	ResourceAccess string `json:"resourceAccess"`
}

// EffectiveStatusSQL returns the database expression used by every account
// entry point. Expired temporary bans become active without a write on the
// high-throughput authentication and gateway paths.
func EffectiveStatusSQL(alias string) string {
	prefix := strings.TrimSpace(alias)
	if prefix != "" {
		prefix += "."
	}
	return "case when " + prefix + "status = 0" +
		" and " + prefix + "ban_permanent = 0" +
		" and " + prefix + "ban_until is not null" +
		" and " + prefix + "ban_until <= now()" +
		" then 1 else " + prefix + "status end"
}

// ActiveTrafficSQL is the shared business-analytics predicate. Raw logs remain
// available for administrator audit, while homepage, dashboard, market, and
// risk aggregates only use traffic owned by an account that is currently active.
func ActiveTrafficSQL(alias string) string {
	prefix := strings.TrimSpace(alias)
	if prefix != "" {
		prefix += "."
	}
	return prefix + "charge_type <> 'ADMIN'" +
		" and exists (select 1 from bp_user bugpk_traffic_user" +
		" where bugpk_traffic_user.id = " + prefix + "user_id" +
		" and " + EffectiveStatusSQL("bugpk_traffic_user") + " = 1)"
}

func NewBanDetails(permanent int, until, reason string) BanDetails {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "未提供封禁原因"
	}
	until = strings.TrimSpace(until)
	banType := BanTypeTemporary
	isPermanent := permanent == 1
	if isPermanent {
		banType = BanTypePermanent
		until = ""
	}
	return BanDetails{
		Tag:            BanTag,
		AccountStatus:  AccountStatusBanned,
		BanType:        banType,
		BanReason:      reason,
		BanUntil:       until,
		Permanent:      isPermanent,
		ResourceAccess: ResourceAccessDeny,
	}
}

func (details BanDetails) Message() string {
	if details.Permanent {
		return "账号已被永久封禁，原因：" + details.BanReason
	}
	if details.BanUntil != "" {
		return "账号已被临时封禁至 " + details.BanUntil + "，原因：" + details.BanReason
	}
	return "账号已被封禁，原因：" + details.BanReason
}

func (details BanDetails) ResponseHeaders() map[string]string {
	headers := map[string]string{
		"X-Bugpk-Account-Status": details.AccountStatus,
		"X-Bugpk-Ban-Type":       details.BanType,
	}
	if details.BanUntil != "" {
		headers["X-Bugpk-Ban-Until"] = details.BanUntil
	}
	return headers
}

func Forbidden(permanent int, until, reason string) *apperrors.BusinessError {
	details := NewBanDetails(permanent, until, reason)
	return apperrors.NewWithData(consts.StatusForbidden, details.Message(), details)
}

// IsBanError reports whether err is a ban rejection rather than any other
// authentication failure. Callers that must enforce a ban without changing the
// existing semantics of expired or malformed credentials need exactly this
// distinction: a 401 has to keep flowing to the handler's refresh path, while a
// 403 carrying ban details must terminate the request here.
func IsBanError(err error) bool {
	if err == nil {
		return false
	}
	business, ok := apperrors.AsBusinessError(err)
	if !ok || business.StatusCode != consts.StatusForbidden {
		return false
	}
	switch business.Data.(type) {
	case BanDetails, *BanDetails:
		return true
	}
	return false
}
