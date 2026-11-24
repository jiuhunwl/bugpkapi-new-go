package riskcontrol

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"bugpk-api/internal/domain/rbac"
	"bugpk-api/internal/pkg/businessclock"
	apperrors "bugpk-api/internal/pkg/errors"
	"bugpk-api/internal/pkg/masking"

	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"github.com/redis/go-redis/v9"
)

type Service struct {
	authz         *rbac.Service
	earthCache    *resultCache[EarthOverview]
	eventsCache   *resultCache[[]RiskEvent]
	homeCache     *resultCache[HomeSnapshot]
	overviewCache *resultCache[Overview]
	rdb           *redis.Client
	repo          *Repository
	now           func() time.Time
}

func NewService(repo *Repository, authz *rbac.Service, redisClients ...*redis.Client) *Service {
	service := &Service{
		authz:         authz,
		earthCache:    newRiskResultCache[EarthOverview](),
		eventsCache:   newRiskResultCache[[]RiskEvent](),
		homeCache:     newRiskHomeResultCache[HomeSnapshot](),
		overviewCache: newRiskResultCache[Overview](),
		repo:          repo,
		now:           time.Now,
	}
	if len(redisClients) > 0 {
		service.rdb = redisClients[0]
	}
	return service
}

func (s *Service) Authenticate(ctx context.Context, token string) (rbac.Principal, error) {
	return s.authz.Authenticate(ctx, token)
}

func (s *Service) BanAccount(ctx context.Context, principal rbac.Principal, req BanAccountRequest) error {
	if err := s.authz.Require(ctx, principal, "risk-control:user:ban"); err != nil {
		return err
	}
	if req.UserID <= 0 {
		return apperrors.New(consts.StatusBadRequest, "用户 ID 不合法")
	}
	if principal.ID == req.UserID {
		return apperrors.New(consts.StatusBadRequest, "不能封禁当前登录用户")
	}
	reason := strings.TrimSpace(req.Reason)
	if reason == "" || len([]rune(reason)) > 100 {
		return apperrors.New(consts.StatusBadRequest, "封禁原因请用一句话说明主要原因（不超过100个字符）")
	}
	mode := strings.ToUpper(strings.TrimSpace(req.Mode))
	var until any
	if mode == "PERMANENT" {
		until = nil
	} else if mode == "TEMPORARY" {
		_, normalized, err := parseBanExpiry(req.BanUntil, s.nowTime())
		if err != nil {
			return apperrors.New(consts.StatusBadRequest, masking.Error(err))
		}
		until = normalized
	} else {
		return apperrors.New(consts.StatusBadRequest, "封禁类型不合法")
	}
	if err := s.repo.ApplyAccountBan(ctx, req.UserID, mode == "PERMANENT", until, reason); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return apperrors.New(consts.StatusNotFound, "用户不存在")
		}
		if errors.Is(err, ErrLastActiveAdmin) {
			return apperrors.New(consts.StatusBadRequest, "不能封禁最后一个可用管理员；请先给其他账号授予管理员角色")
		}
		return err
	}
	s.overviewCache.invalidate()
	s.eventsCache.invalidate()
	// The homepage summary now lives far longer than the interactive entries, so
	// it has to be dropped explicitly: otherwise a ban would keep being reported
	// with the pre-ban risk totals until the long TTL expired.
	s.homeCache.invalidate()
	return nil
}

func (s *Service) nowTime() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

func parseBanExpiry(raw string, now time.Time) (time.Time, string, error) {
	now = now.In(businessclock.Location())
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, "", errors.New("临时封禁必须设置结束时间")
	}
	formats := []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05", "2006-01-02T15:04:05"}
	var parsed time.Time
	var err error
	for _, format := range formats {
		if strings.Contains(format, "Z07") {
			parsed, err = time.Parse(format, raw)
		} else {
			parsed, err = time.ParseInLocation(format, raw, now.Location())
		}
		if err == nil {
			break
		}
	}
	if err != nil || !parsed.Truncate(time.Second).After(now) {
		return time.Time{}, "", errors.New("临时封禁结束时间必须晚于当前时间")
	}
	if parsed.After(now.Add(365 * 24 * time.Hour)) {
		return time.Time{}, "", errors.New("临时封禁时长不能超过 365 天")
	}
	parsed = parsed.In(businessclock.Location()).Truncate(time.Second)
	return parsed, parsed.Format("2006-01-02 15:04:05"), nil
}

func (s *Service) Overview(ctx context.Context, principal rbac.Principal, query RiskQuery) (Overview, error) {
	if err := s.authz.Require(ctx, principal, "risk-control:view"); err != nil {
		return Overview{}, err
	}
	result, err := s.loadOverview(ctx, query)
	if err != nil {
		return Overview{}, err
	}
	if s.authz.Require(ctx, principal, "risk-control:event:list") != nil {
		result.Events = nil
		result.TopFailedIPs = nil
		result.TopFailedAccounts = nil
		result.TopAttackIP = ""
		result.TopAttackParam = ""
	}
	return result, nil
}

func (s *Service) loadOverview(ctx context.Context, query RiskQuery) (Overview, error) {
	query = normalizeQuery(query)
	return s.overviewCache.getOrLoad(ctx, query, func(loadCtx context.Context) (Overview, error) {
		var result Overview
		if useOverviewSnapshot(query) {
			snapshot, found, err := s.repo.LoadOverviewSnapshot(loadCtx, query)
			if err != nil {
				return Overview{}, err
			}
			if found {
				result = snapshot
			} else {
				result, err = s.loadLiveOverview(loadCtx, query)
				if err != nil {
					return Overview{}, err
				}
				_ = s.repo.SaveOverviewSnapshot(loadCtx, result)
			}
		} else {
			var err error
			result, err = s.loadLiveOverview(loadCtx, query)
			if err != nil {
				return Overview{}, err
			}
		}
		attempts, blocked, err := s.invalidKeyMetrics(loadCtx, query.WindowMinutes)
		if err != nil {
			return Overview{}, err
		}
		result.InvalidKeyAttempts = attempts
		result.InvalidKeyBlocked = blocked
		result.RiskScore = overviewRiskScore(result)
		return result, nil
	})
}

func (s *Service) loadLiveOverview(ctx context.Context, query RiskQuery) (Overview, error) {
	events, err := s.loadEvents(ctx, query)
	if err != nil {
		return Overview{}, err
	}
	return s.repo.overviewWithEvents(ctx, query, events)
}

func (s *Service) invalidKeyMetrics(ctx context.Context, windowMinutes int) (int64, int64, error) {
	if s.rdb == nil {
		return 0, 0, nil
	}
	now := time.Now()
	if s.now != nil {
		now = s.now()
	}
	keys := invalidKeyMetricKeys(now, windowMinutes)
	if len(keys) == 0 {
		return 0, 0, nil
	}
	values, err := s.rdb.MGet(ctx, keys...).Result()
	if err != nil {
		return 0, 0, apperrors.Wrap(consts.StatusServiceUnavailable, "risk-control metrics are temporarily unavailable", err)
	}
	var attempts, blocked int64
	for index, value := range values {
		if value == nil {
			continue
		}
		count, parseErr := strconv.ParseInt(fmt.Sprint(value), 10, 64)
		if parseErr != nil || count < 0 {
			continue
		}
		if index%2 == 0 {
			attempts += count
		} else {
			blocked += count
		}
	}
	return attempts, blocked, nil
}

func invalidKeyMetricKeys(now time.Time, windowMinutes int) []string {
	if windowMinutes <= 0 {
		return nil
	}
	keys := make([]string, 0, windowMinutes*2)
	for offset := 0; offset < windowMinutes; offset++ {
		minute := now.Add(-time.Duration(offset)*time.Minute).Unix() / 60
		keys = append(keys,
			fmt.Sprintf("gateway:risk:invalid-key:metric:attempts:%d", minute),
			fmt.Sprintf("gateway:risk:invalid-key:metric:blocked:%d", minute),
		)
	}
	return keys
}

func (s *Service) Events(ctx context.Context, principal rbac.Principal, query RiskQuery) ([]RiskEvent, error) {
	if err := s.authz.Require(ctx, principal, "risk-control:event:list"); err != nil {
		return nil, err
	}
	return s.loadEvents(ctx, query)
}

func (s *Service) loadEvents(ctx context.Context, query RiskQuery) ([]RiskEvent, error) {
	query = normalizeQuery(query)
	return s.eventsCache.getOrLoad(ctx, query, func(loadCtx context.Context) ([]RiskEvent, error) {
		return s.repo.RiskEvents(loadCtx, query)
	})
}

func (s *Service) Earth(ctx context.Context, principal rbac.Principal, query RiskQuery) (EarthOverview, error) {
	if err := s.authz.Require(ctx, principal, "risk-control:earth:view"); err != nil {
		return EarthOverview{}, err
	}
	result, err := s.loadEarth(ctx, query)
	if err != nil {
		return EarthOverview{}, err
	}
	if s.authz.Require(ctx, principal, "risk-control:event:list") != nil {
		result.Events = nil
	}
	return result, nil
}

func (s *Service) loadEarth(ctx context.Context, query RiskQuery) (EarthOverview, error) {
	query = normalizeQuery(query)
	return s.earthCache.getOrLoad(ctx, query, func(loadCtx context.Context) (EarthOverview, error) {
		events, err := s.loadEvents(loadCtx, query)
		if err != nil {
			return EarthOverview{}, err
		}
		return s.repo.earthWithEvents(loadCtx, query, events)
	})
}

func (s *Service) HomeSnapshot(ctx context.Context) (HomeSnapshot, error) {
	query := RiskQuery{Limit: maxLimit, WindowMinutes: maxWindowMinutes}
	return s.homeCache.getOrLoad(ctx, query, func(loadCtx context.Context) (HomeSnapshot, error) {
		return s.repo.HomeSnapshot(loadCtx, query)
	})
}
