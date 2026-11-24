package gateway

import (
	"context"
	"fmt"
	"strings"
	"time"

	"bugpk-api/internal/pkg/businessclock"
	"bugpk-api/internal/pkg/httpclient"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

const (
	degradeWindowDay   = "day"
	degradeWindowWeek  = "week"
	degradeWindowMonth = "month"
	degradeWindowTotal = "total"

	// 累计窗口使用滑动 TTL：活跃用户每次调用都会续期，不会重置；只有连续
	// 一年不调用才过期，用于清理废弃的配置版本和长期不活跃用户。
	degradeTotalTTL = 365 * 24 * time.Hour

	degradeWarnThrottle = time.Minute
)

// degradeIncrementScript 原子地自增计数并按窗口设置 TTL。
// ARGV[2] = 1 时每次写入都刷新 TTL（累计窗口），否则只在键没有 TTL 时设置。
var degradeIncrementScript = redis.NewScript(`
redis.call("INCR", KEYS[1])
local ttl = tonumber(ARGV[1])
if ttl > 0 then
  if ARGV[2] == "1" then
    redis.call("PEXPIRE", KEYS[1], ttl)
  else
    local current = redis.call("PTTL", KEYS[1])
    if current < 0 then
      redis.call("PEXPIRE", KEYS[1], ttl)
    end
  end
end
return 1
`)

// resolveDegradeExecution 判断本次请求是否已达到降级阈值。
//
// 返回的 rule 即使未触发降级也会返回（只要用户组配置了有效阈值），调用方据此
// 在请求结束后计数；target 仅在 degraded=true 时被使用。任何依赖异常都按
// fail-open 处理：继续走原接口，不影响用户调用。
func (s *Service) resolveDegradeExecution(
	ctx context.Context,
	key KeyAuth,
	api InterfaceConfig,
	req OpenRequest,
) (target InterfaceConfig, rule InterfaceDegradeRule, configured bool, degraded bool) {
	if api.Degrade.Enabled != 1 || key.RoleKey == "admin" {
		return api, InterfaceDegradeRule{}, false, false
	}
	rule, ok := api.Degrade.RoleRules[key.RoleID]
	if !ok || rule.Threshold <= 0 {
		return api, InterfaceDegradeRule{}, false, false
	}
	configured = true
	if s.rdb == nil {
		return api, rule, configured, false
	}
	count, err := s.degradeCount(ctx, key.UserID, api, rule)
	if err != nil {
		s.warnDegradeUnavailable("read counter", err)
		return api, rule, configured, false
	}
	if count < rule.Threshold {
		return api, rule, configured, false
	}
	// 规则级目标优先，只有规则未覆盖时才回落到接口默认目标。
	// 规则级接口目标存在时，不能被接口默认的自定义地址抢走。
	targetURL := strings.TrimSpace(rule.TargetURL)
	targetCode := strings.TrimSpace(rule.TargetAPICode)
	if targetURL == "" && targetCode == "" {
		targetURL = strings.TrimSpace(api.Degrade.DefaultTargetURL)
		if targetURL == "" {
			targetCode = strings.TrimSpace(api.Degrade.DefaultTargetAPICode)
		}
	}
	// 自定义内部地址：和轮询的上游节点一样，不需要先注册成公开接口。
	if targetURL != "" {
		custom, err := s.customDegradeTarget(api, targetURL)
		if err != nil {
			s.warnDegradeUnavailable("invalid custom target url", err)
			return api, rule, configured, false
		}
		return custom, rule, configured, true
	}
	if targetCode == "" {
		s.warnDegradeUnavailable("target api code is empty", nil)
		return api, rule, configured, false
	}
	target, err = s.interfaceByCode(ctx, targetCode)
	if err != nil {
		s.warnDegradeUnavailable("load target interface", err)
		return api, rule, configured, false
	}
	if target.ID <= 0 || target.ID == api.ID || target.Status != 1 {
		s.warnDegradeUnavailable("target interface is unavailable", nil)
		return api, rule, configured, false
	}
	if err := s.decryptProxyPoolPassword(&target); err != nil {
		s.warnDegradeUnavailable("decrypt target proxy password", err)
		return api, rule, configured, false
	}
	routed, _, routeErr := resolveDomainRoute(target, req)
	if routeErr != nil {
		s.warnDegradeUnavailable("resolve target domain route", routeErr)
		return api, rule, configured, false
	}
	// 目标接口的响应不能写入源接口的响应缓存，否则降级结束后仍可能返回
	// 目标接口的旧数据。
	routed.CacheEnabled = 0
	routed.degradeTargetKind = "interface"
	return routed, rule, configured, true
}

// customDegradeTarget 把一个自定义内部地址包装成可执行的上游配置。
// 清洗、代理池、超时和轮询校验沿用源接口，只替换上游地址并关闭轮询和缓存，
// 与「接口轮询」里直接填写上游节点地址的行为一致。
func (s *Service) customDegradeTarget(api InterfaceConfig, targetURL string) (InterfaceConfig, error) {
	if err := httpclient.ValidateURL(targetURL); err != nil {
		return InterfaceConfig{}, err
	}
	custom := api
	custom.RequestURL = targetURL
	custom.UpstreamConfigs = nil
	custom.PollingEnabled = 0
	custom.PollingMode = "SINGLE"
	custom.DomainRouting = DomainRoutingConfig{}
	custom.CacheEnabled = 0
	custom.degradeTargetKind = "custom"
	return custom, nil
}

// degradeCount reads the current per-user counter for the source interface.
func (s *Service) degradeCount(ctx context.Context, userID int64, api InterfaceConfig, rule InterfaceDegradeRule) (int64, error) {
	bucket, _, _ := degradeWindowSpec(s.currentTime(), rule.Window)
	key := degradeCounterKey(api.ID, userID, api.Degrade.PolicyVersion, rule.Window, bucket)
	value, err := s.rdb.Get(ctx, key).Int64()
	if err == redis.Nil {
		return 0, nil
	}
	return value, err
}

// recordDegradeAttempt increments the counter once for every admitted request.
// 计数失败只记 warn，不影响已经完成的响应。
func (s *Service) recordDegradeAttempt(ctx context.Context, userID int64, api InterfaceConfig, rule InterfaceDegradeRule) {
	if s.rdb == nil || rule.Threshold <= 0 {
		return
	}
	now := s.currentTime()
	bucket, ttl, refreshTTL := degradeWindowSpec(now, rule.Window)
	key := degradeCounterKey(api.ID, userID, api.Degrade.PolicyVersion, rule.Window, bucket)
	ttlMillis := int64(0)
	if ttl > 0 {
		ttlMillis = ttl.Milliseconds()
	}
	commitCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), quotaCommitTimeout)
	defer cancel()
	if _, err := degradeIncrementScript.Run(commitCtx, s.rdb, []string{key}, ttlMillis, boolInt(refreshTTL)).Result(); err != nil {
		s.warnDegradeUnavailable("increment counter", err)
	}
}

// degradeCounterKey builds the per-source-interface, per-user counter key.
// policy_version is part of the key so a material config change starts a fresh
// count without having to enumerate every user.
func degradeCounterKey(interfaceID, userID, policyVersion int64, window, bucket string) string {
	if policyVersion < 1 {
		policyVersion = 1
	}
	return fmt.Sprintf("openapi:degrade:v1:%d:%d:%d:%s:%s", interfaceID, policyVersion, userID, window, bucket)
}

// degradeWindowSpec returns the fixed bucket, TTL until the bucket ends (plus a
// small grace period), and whether the TTL should be refreshed on every write.
func degradeWindowSpec(now time.Time, window string) (bucket string, ttl time.Duration, refreshTTL bool) {
	local := now.In(businessclock.Location())
	startOfDay := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, local.Location())
	switch strings.ToLower(strings.TrimSpace(window)) {
	case degradeWindowWeek:
		startOfWeek := startOfDay.AddDate(0, 0, -isoWeekdayOffset(local.Weekday()))
		end := startOfWeek.AddDate(0, 0, 7)
		return startOfWeek.Format("20060102"), end.Sub(now) + 2*time.Hour, false
	case degradeWindowMonth:
		startOfMonth := time.Date(local.Year(), local.Month(), 1, 0, 0, 0, 0, local.Location())
		end := startOfMonth.AddDate(0, 1, 0)
		return local.Format("200601"), end.Sub(now) + 24*time.Hour, false
	case degradeWindowTotal:
		return "total", degradeTotalTTL, true
	default:
		end := startOfDay.AddDate(0, 0, 1)
		return local.Format("20060102"), end.Sub(now) + time.Hour, false
	}
}

func (s *Service) decryptProxyPoolPassword(api *InterfaceConfig) error {
	if api == nil || strings.TrimSpace(api.ProxyPoolConfig.Password) == "" {
		return nil
	}
	password, err := s.keyCipher.DecryptCompatible(api.ProxyPoolConfig.Password)
	if err != nil {
		return err
	}
	api.ProxyPoolConfig.Password = password
	return nil
}

func (s *Service) warnDegradeUnavailable(reason string, err error) {
	if s.log == nil {
		return
	}
	now := s.currentTime().UnixNano()
	last := s.degradeWarnAt.Load()
	if last > 0 && now-last < degradeWarnThrottle.Nanoseconds() {
		return
	}
	if !s.degradeWarnAt.CompareAndSwap(last, now) {
		return
	}
	fields := []zap.Field{zap.String("reason", reason)}
	if err != nil {
		fields = append(fields, zap.String("error", err.Error()))
	}
	s.log.Warn("gateway degrade unavailable, serving original interface", fields...)
}
