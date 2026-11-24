package gateway

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"bugpk-api/internal/pkg/businessclock"
	"bugpk-api/internal/pkg/dbx"
	apperrors "bugpk-api/internal/pkg/errors"
	"bugpk-api/internal/pkg/masking"

	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

// 用户组阶梯配额（免费额度）与付费额度（套餐 / 点数 / 余额）。
//
// 角色（bp_role）是平台上的「用户组」，每个用户通过 primary_role_id 落到一个
// 角色。角色给的是免费额度，分四层固定窗口（分/时/日/月），任一窗口超限即表示
// 免费额度已用尽；免费额度用尽之后才由付费额度（套餐 / 点数 / 余额）承接，
// 两层各自独立计数，不合并成一个数字：
//
//	免费额度：用户组给的次数，按窗口重置，用不完就作废；
//	付费额度：用户买到的次数，按套餐有效期存续，不随用户组重置。
//
// 另有一条注册后 24h 的首日额度，与角色无关，专门压住「开新号换免费额度」的成本。
//
// 计量口径由角色决定：
//
//	attempt —— 请求通过鉴权与参数校验后立即计量，上游失败也计；
//	success —— 先检查、请求成功返回后再计量，失败不占额度。
//
// 计数全部走 Redis Lua，固定窗口与 bucket 键一致；Redis 不可用时 fail-closed。
const (
	quotaCountModeAttempt = "attempt"
	quotaCountModeSuccess = "success"

	quotaWindowMinute     = "minute"
	quotaWindowHour       = "hour"
	quotaWindowDay        = "day"
	quotaWindowWeek       = "week"
	quotaWindowMonth      = "month"
	quotaWindowNewAccount = "new-account"
	quotaWindowPerUser    = "user"
	quotaWindowAllUsers   = "all"

	quotaCommitTimeout = 2 * time.Second
)

// quotaAttemptScript 原子地检查并消费所有窗口。返回 0 表示放行，返回 i 表示
// 第 i 个窗口已超限（调用方据此生成文案）。先检查全部再自增，避免前几个窗口
// 被扣了、后面窗口却拒绝的「半消费」状态。
var quotaAttemptScript = redis.NewScript(`
for i = 1, #KEYS do
  local limit = tonumber(ARGV[i])
  local current = tonumber(redis.call("GET", KEYS[i]) or "0")
  if limit > 0 and current >= limit then
    return i
  end
end
for i = 1, #KEYS do
  redis.call("INCR", KEYS[i])
  if redis.call("PTTL", KEYS[i]) < 0 then
    redis.call("PEXPIRE", KEYS[i], tonumber(ARGV[#KEYS + i]))
  end
end
return 0
`)

// quotaCheckScript 只检查不消费，用于「仅成功计量」模式。
var quotaCheckScript = redis.NewScript(`
for i = 1, #KEYS do
  local limit = tonumber(ARGV[i])
  local current = tonumber(redis.call("GET", KEYS[i]) or "0")
  if limit > 0 and current >= limit then
    return i
  end
end
return 0
`)

// quotaCommitScript 在请求成功后补记一次。TTL 仅在键没有过期时间时设置，
// 避免同一 bucket 内被反复延长。
var quotaCommitScript = redis.NewScript(`
for i = 1, #KEYS do
  redis.call("INCR", KEYS[i])
  if redis.call("PTTL", KEYS[i]) < 0 then
    redis.call("PEXPIRE", KEYS[i], tonumber(ARGV[i]))
  end
end
return 1
`)

type quotaWindowSpec struct {
	Key   string
	Limit int64
	Name  string
	TTL   time.Duration
}

type requestQuotaReservation struct {
	Mode    string
	Windows []quotaWindowSpec
	// Billing 非零时表示本次请求已由套餐额度承接；Open 会复用它完成 QPS 与确认/释放，
	// 不再走 reserveForOpen（零价接口在那里只会得到 NO_CHARGE/FREE）。
	Billing BillingReservation
}

// reserveRequestQuota 是网关的配额闸门，按「套餐额度 → 用户组免费额度」两层裁决。
//
// 第一层（套餐额度）：用户名下有可用套餐时先扣套餐。显式指定的主套餐
// （is_primary=1）排最前，其余按到期时间最早优先；这一层不再消耗用户组免费次数。
//
// 第二层（免费额度）：套餐全部用尽后才按用户组分/时/日/月窗口计数；窗口打满且
// 无其他付费途径时 429。有价格的接口在第二层之后仍由计费链路扣点数/余额。
func (s *Service) reserveRequestQuota(ctx context.Context, key KeyAuth, api InterfaceConfig) (requestQuotaReservation, error) {
	if key.RoleKey == "admin" {
		return requestQuotaReservation{}, nil
	}
	// 套餐优先：主套餐排最前，其余按到期最早；打满后再回落到用户组免费额度。
	// package_mode=skip 的接口整层跳过，一次套餐都不占。
	if api.consumesPackageQuota() && (key.HasUsableGlobalPackage || key.HasUsableInterfacePackage) {
		if packageReservation, ok, err := s.reservePackageQuota(ctx, api, key); err != nil {
			return requestQuotaReservation{}, err
		} else if ok {
			return requestQuotaReservation{Billing: packageReservation}, nil
		}
	}
	// 免费接口没有可消耗的付费途径（套餐与点数都不会被扣），第二层永远不成立：
	// 给它放行等于让套餐用户对免费接口无限调用，闸门会彻底失效。
	paidCapable := !api.assetsFree()
	windows := s.requestQuotaWindows(key, api)
	if len(windows) == 0 {
		return requestQuotaReservation{}, nil
	}
	if s.rdb == nil {
		s.warnQuotaUnavailable("redis client is not configured")
		return requestQuotaReservation{}, apperrors.New(consts.StatusServiceUnavailable, "请求配额服务暂时不可用（计数器不可达），请稍后重试")
	}
	mode := key.QuotaCountMode
	if mode != quotaCountModeSuccess {
		mode = quotaCountModeAttempt
	}
	keys := make([]string, len(windows))
	args := make([]any, 0, len(windows)*2)
	for index, window := range windows {
		keys[index] = window.Key
		args = append(args, window.Limit)
	}
	if mode == quotaCountModeAttempt {
		// 脚本先检查全部窗口再自增，被拒时一个窗口都不会被扣：因此「免费额度
		// 打满」这一步本身不会污染计数，可以放心在拒绝后转付费承接。
		rejected, err := quotaAttemptScript.Run(ctx, s.rdb, keys, append(args, ttlArgs(windows)...)...).Int64()
		if err != nil {
			s.warnQuotaUnavailable(err.Error())
			return requestQuotaReservation{}, apperrors.New(consts.StatusServiceUnavailable, "请求配额服务暂时不可用（计数器不可达），请稍后重试")
		}
		if rejected == 0 {
			return requestQuotaReservation{Mode: mode}, nil
		}
		if paidCapable && s.paidEntitlementAvailable(ctx, key.UserID, api.ID) {
			return requestQuotaReservation{}, nil
		}
		return requestQuotaReservation{}, quotaRejection(windows[rejected-1])
	}
	rejected, err := quotaCheckScript.Run(ctx, s.rdb, keys, args...).Int64()
	if err != nil {
		s.warnQuotaUnavailable(err.Error())
		return requestQuotaReservation{}, apperrors.New(consts.StatusServiceUnavailable, "请求配额服务暂时不可用（计数器不可达），请稍后重试")
	}
	if rejected == 0 {
		// 仅成功计量：窗口随 reservation 返回，请求成功后再补记。
		return requestQuotaReservation{Mode: mode, Windows: windows}, nil
	}
	if paidCapable && s.paidEntitlementAvailable(ctx, key.UserID, api.ID) {
		// 付费承接的请求不携带窗口，成功后不会回写免费额度计数。
		return requestQuotaReservation{}, nil
	}
	return requestQuotaReservation{}, quotaRejection(windows[rejected-1])
}

// reservePackageQuota 用套餐额度承接请求；预占失败（打满/与接口不匹配）时返回
// ok=false，由调用方回落到用户组免费额度。
func (s *Service) reservePackageQuota(ctx context.Context, api InterfaceConfig, key KeyAuth) (BillingReservation, bool, error) {
	if s.repo == nil {
		return BillingReservation{}, false, nil
	}
	reservation, ok, err := s.repo.ReservePackageQuota(ctx, api, key.ID, key.UserID)
	if err == nil {
		return reservation, ok, nil
	}
	if errors.Is(err, dbx.ErrCommitUncertain) && reservation.Reserved && reservation.BillingRecordID > 0 {
		releaseErr := s.releaseReservedBilling(ctx, reservation, key.UserID, "package quota reservation commit outcome uncertain")
		if releaseErr != nil {
			return BillingReservation{}, false, errors.Join(err, releaseErr)
		}
	}
	return BillingReservation{}, false, err
}

// commitRequestQuota 仅处理「仅成功计量」模式；attempt 模式已在闸门处消费。
// 补记失败不反向影响已经成功的调用，但会记录错误便于对账。
func (s *Service) commitRequestQuota(ctx context.Context, reservation requestQuotaReservation) {
	if reservation.Mode != quotaCountModeSuccess || len(reservation.Windows) == 0 || s.rdb == nil {
		return
	}
	commitCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), quotaCommitTimeout)
	defer cancel()
	keys := make([]string, len(reservation.Windows))
	args := make([]any, len(reservation.Windows))
	for index, window := range reservation.Windows {
		keys[index] = window.Key
		args[index] = window.TTL.Milliseconds()
	}
	if _, err := quotaCommitScript.Run(commitCtx, s.rdb, keys, args...).Result(); err != nil && s.log != nil {
		s.log.Warn("gateway request quota commit failed",
			zap.Int("windows", len(reservation.Windows)),
			zap.String("error", err.Error()),
		)
	}
}

func ttlArgs(windows []quotaWindowSpec) []any {
	args := make([]any, len(windows))
	for index, window := range windows {
		args[index] = window.TTL.Milliseconds()
	}
	return args
}

// paidEntitlementAvailable 判断免费额度打满之后，本次调用能否转由付费额度承接。
//
// 探测失败按「没有付费权益」处理：宁可让这一次请求拿到 429（用户重试即可），
// 也不让数据库抖动变成绕过免费额度的口子。真正的扣减仍在 reserveBillingTx 里
// 重新校验一次，这里只是提前分流的判断。
//
// Service 在纯配额单测里没有仓储，此时一律按「没有付费权益」处理。
func (s *Service) paidEntitlementAvailable(ctx context.Context, userID, apiID int64) bool {
	if s.repo == nil {
		return false
	}
	cacheKey := paidEntitlementKey{userID: userID, interfaceID: apiID}
	now := s.currentTime()
	if cached, ok := s.paidEntitlement.Load(cacheKey); ok {
		entry := cached.(paidEntitlementEntry)
		if now.Before(entry.expiresAt) {
			return entry.available
		}
		s.paidEntitlement.Delete(cacheKey)
		s.paidEntitlementCount.Add(-1)
	}
	available, err := s.repo.HasPaidEntitlement(ctx, userID, apiID)
	if err != nil {
		// 失败结果不入缓存：DB 恢复后下一秒就该放行，不能被负缓存拖延。
		if s.log != nil {
			s.log.Warn("gateway paid entitlement probe failed",
				zap.Int64("user_id", userID),
				zap.Int64("interface_id", apiID),
				zap.String("error", masking.Error(err)),
			)
		}
		return false
	}
	ttl := paidEntitlementNegativeTTL
	if available {
		ttl = paidEntitlementPositiveTTL
	}
	// 上限保护：只在总量可控时写入，读路径不受影响。
	if s.paidEntitlementCount.Load() < paidEntitlementCacheLimit {
		s.paidEntitlement.Store(cacheKey, paidEntitlementEntry{available: available, expiresAt: now.Add(ttl)})
		s.paidEntitlementCount.Add(1)
	}
	return available
}

// 正负缓存分开设 TTL：命中付费途径的用户量大且稳定，给 10s 减少查询；
// 负结果只给 5s，购买成功后最多 5 秒就能生效，用户几乎感知不到。
const (
	paidEntitlementPositiveTTL = 10 * time.Second
	paidEntitlementNegativeTTL = 5 * time.Second
	paidEntitlementCacheLimit  = int64(8192)
)

type paidEntitlementKey struct {
	userID      int64
	interfaceID int64
}

type paidEntitlementEntry struct {
	available bool
	expiresAt time.Time
}

// requestQuotaWindows 组装用户组免费额度窗口。api 只用于 group_mode：
// skip 时不建角色窗口、count 时即使角色关闭配额也建窗口，而首日额度是注册防刷
// 闸门，任何配置都不能跳过它。读额度概览等没有接口上下文的调用方传零值
// InterfaceConfig，等价于 group_mode=inherit，行为与改动前一致。
func (s *Service) requestQuotaWindows(key KeyAuth, api InterfaceConfig) []quotaWindowSpec {
	now := s.currentTime()
	local := now.In(businessclock.Location())
	windows := make([]quotaWindowSpec, 0, 5)
	add := func(name string, limit int64, bucket string, end time.Time, grace time.Duration) {
		if limit <= 0 {
			return
		}
		windows = append(windows, quotaWindowSpec{
			Key:   fmt.Sprintf("openapi:quota:request:v1:%d:%s:%s", key.UserID, name, bucket),
			Limit: limit,
			Name:  name,
			TTL:   end.Sub(now) + grace,
		})
	}
	startOfMinute := time.Date(local.Year(), local.Month(), local.Day(), local.Hour(), local.Minute(), 0, 0, local.Location())
	startOfHour := time.Date(local.Year(), local.Month(), local.Day(), local.Hour(), 0, 0, 0, local.Location())
	startOfDay := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, local.Location())
	startOfMonth := time.Date(local.Year(), local.Month(), 1, 0, 0, 0, 0, local.Location())
	if api.countsGroupQuota() && (key.QuotaEnabled == 1 || api.forcesGroupQuota()) {
		add(quotaWindowMinute, key.QuotaMinuteLimit, local.Format("200601021504"), startOfMinute.Add(time.Minute), time.Minute)
		add(quotaWindowHour, key.QuotaHourLimit, local.Format("2006010215"), startOfHour.Add(time.Hour), 5*time.Minute)
		add(quotaWindowDay, key.QuotaDayLimit, local.Format("20060102"), startOfDay.AddDate(0, 0, 1), time.Hour)
		add(quotaWindowMonth, key.QuotaMonthLimit, local.Format("200601"), startOfMonth.AddDate(0, 1, 0), 24*time.Hour)
	}
	// 首日额度是注册防刷闸门，独立于用户组配额开关：即使某个用户组关闭了
	// 总量配额，新账号 24h 内的白嫖上限仍然生效。
	if key.NewAccountDayLimit > 0 && !key.UserCreatedAt.IsZero() {
		registeredAt := key.UserCreatedAt.In(businessclock.Location())
		until := registeredAt.Add(24 * time.Hour)
		if until.After(now) {
			windows = append(windows, quotaWindowSpec{
				Key:   fmt.Sprintf("openapi:quota:request:v1:%d:%s", key.UserID, quotaWindowNewAccount),
				Limit: key.NewAccountDayLimit,
				Name:  quotaWindowNewAccount,
				TTL:   until.Sub(now) + time.Minute,
			})
		}
	}
	return windows
}

// reserveInterfaceQuota 是单接口小/天/周/月配额闸门。它独立于用户组总量：
// 用户组限制「账号总量」，这里限制「单接口单账号」和「单接口全平台总量」。
//
// 角色覆盖默认只作用于单用户上限（inherit / unlimited / adjust / override），
// 全平台总量始终按接口生效；只有 ignore 例外——它连全平台计数一起跳过，
// 属于「整层豁免」，与 unlimited 必须区分开。
//
// packageCovered 表示本次请求已由套餐额度承接。接口的 cover_package=0（默认）
// 时整层跳过：不检查也不计数，保留「套餐优先」；设为 1 时接口配额同样约束
// 套餐用户。
func (s *Service) reserveInterfaceQuota(ctx context.Context, key KeyAuth, api InterfaceConfig, packageCovered bool) (requestQuotaReservation, error) {
	if api.QuotaEnabled != 1 {
		return requestQuotaReservation{}, nil
	}
	if api.QuotaCoverPackage != 1 && packageCovered {
		return requestQuotaReservation{}, nil
	}
	override := api.RoleQuotas[key.RoleID]
	if strings.EqualFold(strings.TrimSpace(override.Mode), interfaceRoleModeIgnore) {
		// ignore 是整层跳过：不检查也不计数，全平台总量一并免除。
		// 与 unlimited 的区别就在全平台这一层——unlimited 只放开单用户上限。
		return requestQuotaReservation{}, nil
	}
	perUser := effectiveInterfaceUserLimits(api.QuotaPerUser, override)
	windows := s.interfaceQuotaWindows(api.ID, key.UserID, perUser, api.QuotaAllUsers)
	if len(windows) == 0 {
		return requestQuotaReservation{}, nil
	}
	if s.rdb == nil {
		s.warnQuotaUnavailable("redis client is not configured")
		return requestQuotaReservation{}, apperrors.New(consts.StatusServiceUnavailable, "接口配额服务暂时不可用（计数器不可达），请稍后重试")
	}
	mode := api.QuotaCountMode
	if mode != quotaCountModeSuccess {
		mode = quotaCountModeAttempt
	}
	keys := make([]string, len(windows))
	args := make([]any, 0, len(windows)*2)
	for index, window := range windows {
		keys[index] = window.Key
		args = append(args, window.Limit)
	}
	if mode == quotaCountModeAttempt {
		rejected, err := quotaAttemptScript.Run(ctx, s.rdb, keys, append(args, ttlArgs(windows)...)...).Int64()
		if err != nil {
			s.warnQuotaUnavailable(err.Error())
			return requestQuotaReservation{}, apperrors.New(consts.StatusServiceUnavailable, "接口配额服务暂时不可用（计数器不可达），请稍后重试")
		}
		if rejected > 0 {
			return requestQuotaReservation{}, interfaceQuotaRejection(api.APICode, windows[rejected-1])
		}
		return requestQuotaReservation{Mode: mode}, nil
	}
	rejected, err := quotaCheckScript.Run(ctx, s.rdb, keys, args...).Int64()
	if err != nil {
		s.warnQuotaUnavailable(err.Error())
		return requestQuotaReservation{}, apperrors.New(consts.StatusServiceUnavailable, "接口配额服务暂时不可用（计数器不可达），请稍后重试")
	}
	if rejected > 0 {
		return requestQuotaReservation{}, interfaceQuotaRejection(api.APICode, windows[rejected-1])
	}
	return requestQuotaReservation{Mode: mode, Windows: windows}, nil
}

// effectiveInterfaceUserLimits applies a role override to the interface default
// per-user caps. 0 means unlimited at every layer:
//   - base 0 + adjust => stays unlimited (negative delta cannot invent a finite cap);
//   - finite base + negative delta that reaches 0 or below => 1, never "unlimited".
func effectiveInterfaceUserLimits(base InterfaceWindowLimits, override InterfaceRoleQuota) InterfaceWindowLimits {
	switch strings.ToLower(strings.TrimSpace(override.Mode)) {
	case interfaceRoleModeUnlimited:
		return InterfaceWindowLimits{}
	case interfaceRoleModeIgnore:
		// 正常情况下 reserveInterfaceQuota 已在构造窗口前短路；这里再返回全 0，
		// 保证任何复用本函数的调用方都不会因为 ignore 而误用接口默认值。
		return InterfaceWindowLimits{}
	case "adjust":
		return InterfaceWindowLimits{
			Hour:  adjustInterfaceLimit(base.Hour, override.Adjust.Hour),
			Day:   adjustInterfaceLimit(base.Day, override.Adjust.Day),
			Week:  adjustInterfaceLimit(base.Week, override.Adjust.Week),
			Month: adjustInterfaceLimit(base.Month, override.Adjust.Month),
		}
	case "override":
		return InterfaceWindowLimits{
			Hour:  override.Override.Hour,
			Day:   override.Override.Day,
			Week:  override.Override.Week,
			Month: override.Override.Month,
		}
	default:
		return base
	}
}

func adjustInterfaceLimit(base, delta int64) int64 {
	if base <= 0 {
		return 0
	}
	adjusted := base + delta
	if adjusted < 1 {
		return 1
	}
	if adjusted > maxInterfaceQuotaLimit {
		return maxInterfaceQuotaLimit
	}
	return adjusted
}

const maxInterfaceQuotaLimit int64 = 1_000_000_000_000

// 用户组覆盖模式。ignore 是 1.2.101 新增的「整层跳过」，与 unlimited 的差别
// 见 reserveInterfaceQuota：unlimited 仍然消耗全平台总量，ignore 连计数都不发生。
const (
	interfaceRoleModeUnlimited = "unlimited"
	interfaceRoleModeIgnore    = "ignore"
)

func (s *Service) interfaceQuotaWindows(
	interfaceID, userID int64,
	perUser, allUsers InterfaceWindowLimits,
) []quotaWindowSpec {
	now := s.currentTime()
	local := now.In(businessclock.Location())
	startOfHour := time.Date(local.Year(), local.Month(), local.Day(), local.Hour(), 0, 0, 0, local.Location())
	startOfDay := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, local.Location())
	startOfWeek := startOfDay.AddDate(0, 0, -isoWeekdayOffset(local.Weekday()))
	startOfMonth := time.Date(local.Year(), local.Month(), 1, 0, 0, 0, 0, local.Location())
	windows := make([]quotaWindowSpec, 0, 8)
	add := func(scope, window string, limit int64, bucket string, end time.Time, grace time.Duration) {
		if limit <= 0 {
			return
		}
		key := fmt.Sprintf("openapi:quota:interface:v1:%d:%s:%s:%s", interfaceID, scope, window, bucket)
		if scope == quotaWindowPerUser {
			key = fmt.Sprintf("openapi:quota:interface:v1:%d:%s:%d:%s:%s", interfaceID, scope, userID, window, bucket)
		}
		windows = append(windows, quotaWindowSpec{Key: key, Limit: limit, Name: scope + ":" + window, TTL: end.Sub(now) + grace})
	}
	add(quotaWindowPerUser, quotaWindowHour, perUser.Hour, local.Format("2006010215"), startOfHour.Add(time.Hour), 5*time.Minute)
	add(quotaWindowPerUser, quotaWindowDay, perUser.Day, local.Format("20060102"), startOfDay.AddDate(0, 0, 1), time.Hour)
	add(quotaWindowPerUser, quotaWindowWeek, perUser.Week, startOfWeek.Format("20060102"), startOfWeek.AddDate(0, 0, 7), 2*time.Hour)
	add(quotaWindowPerUser, quotaWindowMonth, perUser.Month, local.Format("200601"), startOfMonth.AddDate(0, 1, 0), 24*time.Hour)
	add(quotaWindowAllUsers, quotaWindowHour, allUsers.Hour, local.Format("2006010215"), startOfHour.Add(time.Hour), 5*time.Minute)
	add(quotaWindowAllUsers, quotaWindowDay, allUsers.Day, local.Format("20060102"), startOfDay.AddDate(0, 0, 1), time.Hour)
	add(quotaWindowAllUsers, quotaWindowWeek, allUsers.Week, startOfWeek.Format("20060102"), startOfWeek.AddDate(0, 0, 7), 2*time.Hour)
	add(quotaWindowAllUsers, quotaWindowMonth, allUsers.Month, local.Format("200601"), startOfMonth.AddDate(0, 1, 0), 24*time.Hour)
	return windows
}

// isoWeekdayOffset returns 0 for Monday ... 6 for Sunday.
func isoWeekdayOffset(weekday time.Weekday) int {
	if weekday == time.Sunday {
		return 6
	}
	return int(weekday) - 1
}

func interfaceQuotaRejection(apiCode string, window quotaWindowSpec) error {
	scope, name, _ := strings.Cut(window.Name, ":")
	label := map[string]string{
		quotaWindowHour:  "每小时",
		quotaWindowDay:   "每日",
		quotaWindowWeek:  "每周",
		quotaWindowMonth: "每月",
	}[name]
	if label == "" {
		label = name
	}
	if scope == quotaWindowAllUsers {
		return apperrors.New(consts.StatusTooManyRequests,
			fmt.Sprintf("接口 %s 的全平台调用额度已用尽：%s最多 %d 次，请稍后重试", apiCode, label, window.Limit))
	}
	return apperrors.New(consts.StatusTooManyRequests,
		fmt.Sprintf("接口 %s 的账号调用额度已用尽：%s最多 %d 次，请升级用户组或稍后重试", apiCode, label, window.Limit))
}

func quotaRejection(window quotaWindowSpec) error {
	if window.Name == quotaWindowNewAccount {
		return apperrors.New(consts.StatusTooManyRequests,
			fmt.Sprintf("新账号首日免费额度已用完（上限 %d 次），注册满 24 小时后自动恢复", window.Limit))
	}
	label := map[string]string{
		quotaWindowMinute: "每分钟",
		quotaWindowHour:   "每小时",
		quotaWindowDay:    "每日",
		quotaWindowMonth:  "每月",
	}[window.Name]
	if label == "" {
		label = window.Name
	}
	// 文案必须点明这是「免费额度」，并给出两条出口（买套餐加额度 / 升级用户组），
	// 避免用户以为只能等窗口重置。
	return apperrors.New(consts.StatusTooManyRequests,
		fmt.Sprintf("免费额度已用尽：当前用户组%s最多 %d 次，可购买套餐获得额外额度或升级用户组后继续调用", label, window.Limit))
}

func (s *Service) warnQuotaUnavailable(reason string) {
	if s.log == nil {
		return
	}
	s.log.Error("gateway request quota limiter unavailable, rejecting request fail-closed",
		zap.String("error", reason),
	)
}
