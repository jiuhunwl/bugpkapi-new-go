package interfaceapi

import (
	"context"
	"errors"
	"strings"

	apperrors "bugpk-api/internal/pkg/errors"

	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

const (
	maxInterfaceQuotaLimit    int64 = 1_000_000_000_000
	maxInterfaceRoleOverrides       = 500

	// quotaRoleIgnore 是用户组的「整层忽略」覆盖：该组的请求既不检查也不计数，
	// 连接口全平台总量都不占。与 unlimited 的区别是后者仍会消耗全平台计数。
	quotaRoleIgnore = "ignore"
)

func (s *Service) InterfaceQuota(ctx context.Context, principal Principal, id int64) (InterfaceQuotaPolicy, error) {
	if err := s.require(ctx, principal, "interface:list"); err != nil {
		return InterfaceQuotaPolicy{}, err
	}
	if _, err := s.repo.ByID(ctx, id); err != nil {
		return InterfaceQuotaPolicy{}, mapNotFound(err, "接口不存在")
	}
	return s.repo.InterfaceQuota(ctx, id)
}

func (s *Service) SaveInterfaceQuota(ctx context.Context, principal Principal, id int64, req InterfaceQuotaPolicy) error {
	if err := s.require(ctx, principal, "interface:update"); err != nil {
		return err
	}
	current, err := s.repo.ByID(ctx, id)
	if err != nil {
		return mapNotFound(err, "接口不存在")
	}
	policy, err := normalizeInterfaceQuota(req)
	if err != nil {
		return err
	}
	if err := s.repo.SaveInterfaceQuota(ctx, id, policy); err != nil {
		if errors.Is(err, ErrInterfaceQuotaRoleInvalid) {
			return apperrors.New(consts.StatusBadRequest, "用户组配额引用了不存在的角色")
		}
		return err
	}
	s.notifyInterfaceChanged(current.APICode, current.APICode)
	return nil
}

func normalizeInterfaceQuota(req InterfaceQuotaPolicy) (InterfaceQuotaPolicy, error) {
	if req.Enabled != 0 && req.Enabled != 1 {
		return InterfaceQuotaPolicy{}, apperrors.New(consts.StatusBadRequest, "接口配额开关不合法")
	}
	if req.CoverPackage != 0 && req.CoverPackage != 1 {
		return InterfaceQuotaPolicy{}, apperrors.New(consts.StatusBadRequest, "套餐覆盖开关不合法")
	}
	countMode := strings.ToLower(strings.TrimSpace(req.CountMode))
	if countMode == "" {
		countMode = "attempt"
	}
	if countMode != "attempt" && countMode != "success" {
		return InterfaceQuotaPolicy{}, apperrors.New(consts.StatusBadRequest, "接口配额计量方式只能是 attempt 或 success")
	}
	values := map[string]int64{
		"perUserHour":   req.PerUserHour,
		"perUserDay":    req.PerUserDay,
		"perUserWeek":   req.PerUserWeek,
		"perUserMonth":  req.PerUserMonth,
		"allUsersHour":  req.AllUsersHour,
		"allUsersDay":   req.AllUsersDay,
		"allUsersWeek":  req.AllUsersWeek,
		"allUsersMonth": req.AllUsersMonth,
	}
	for name, value := range values {
		if value < 0 || value > maxInterfaceQuotaLimit {
			return InterfaceQuotaPolicy{}, apperrors.New(consts.StatusBadRequest, "接口配额 "+name+" 超出允许范围")
		}
	}
	if err := validateInterfaceQuotaLadder("单用户", req.PerUserHour, req.PerUserDay, req.PerUserWeek, req.PerUserMonth); err != nil {
		return InterfaceQuotaPolicy{}, err
	}
	if err := validateInterfaceQuotaLadder("全平台", req.AllUsersHour, req.AllUsersDay, req.AllUsersWeek, req.AllUsersMonth); err != nil {
		return InterfaceQuotaPolicy{}, err
	}
	if req.PerUserHour > 0 && req.AllUsersHour > 0 && req.PerUserHour > req.AllUsersHour {
		return InterfaceQuotaPolicy{}, apperrors.New(consts.StatusBadRequest, "单用户每小时配额不能大于全平台每小时配额")
	}
	if req.PerUserDay > 0 && req.AllUsersDay > 0 && req.PerUserDay > req.AllUsersDay {
		return InterfaceQuotaPolicy{}, apperrors.New(consts.StatusBadRequest, "单用户每日配额不能大于全平台每日配额")
	}
	if req.PerUserWeek > 0 && req.AllUsersWeek > 0 && req.PerUserWeek > req.AllUsersWeek {
		return InterfaceQuotaPolicy{}, apperrors.New(consts.StatusBadRequest, "单用户每周配额不能大于全平台每周配额")
	}
	if req.PerUserMonth > 0 && req.AllUsersMonth > 0 && req.PerUserMonth > req.AllUsersMonth {
		return InterfaceQuotaPolicy{}, apperrors.New(consts.StatusBadRequest, "单用户每月配额不能大于全平台每月配额")
	}
	if len(req.RoleOverrides) > maxInterfaceRoleOverrides {
		return InterfaceQuotaPolicy{}, apperrors.New(consts.StatusBadRequest, "用户组覆盖配置不能超过 500 条")
	}
	seen := make(map[int64]struct{}, len(req.RoleOverrides))
	overrides := make([]InterfaceRoleQuota, 0, len(req.RoleOverrides))
	for _, item := range req.RoleOverrides {
		mode := strings.ToLower(strings.TrimSpace(item.Mode))
		if mode == "" || mode == "inherit" {
			continue
		}
		if item.RoleID <= 0 {
			return InterfaceQuotaPolicy{}, apperrors.New(consts.StatusBadRequest, "用户组配额的角色 ID 不合法")
		}
		if _, ok := seen[item.RoleID]; ok {
			return InterfaceQuotaPolicy{}, apperrors.New(consts.StatusBadRequest, "同一角色不能重复配置")
		}
		seen[item.RoleID] = struct{}{}
		switch mode {
		case "unlimited", quotaRoleIgnore, "adjust", "override":
		default:
			return InterfaceQuotaPolicy{}, apperrors.New(consts.StatusBadRequest, "用户组配额模式不合法")
		}
		if mode == "adjust" {
			for _, value := range []int64{item.AdjustHour, item.AdjustDay, item.AdjustWeek, item.AdjustMonth} {
				if value < -maxInterfaceQuotaLimit || value > maxInterfaceQuotaLimit {
					return InterfaceQuotaPolicy{}, apperrors.New(consts.StatusBadRequest, "用户组加减值超出允许范围")
				}
			}
		}
		if mode == "override" {
			for _, value := range []int64{item.OverrideHour, item.OverrideDay, item.OverrideWeek, item.OverrideMonth} {
				if value < 0 || value > maxInterfaceQuotaLimit {
					return InterfaceQuotaPolicy{}, apperrors.New(consts.StatusBadRequest, "用户组覆盖值超出允许范围")
				}
			}
		}
		overrides = append(overrides, InterfaceRoleQuota{
			RoleID:        item.RoleID,
			Mode:          mode,
			AdjustHour:    item.AdjustHour,
			AdjustDay:     item.AdjustDay,
			AdjustWeek:    item.AdjustWeek,
			AdjustMonth:   item.AdjustMonth,
			OverrideHour:  item.OverrideHour,
			OverrideDay:   item.OverrideDay,
			OverrideWeek:  item.OverrideWeek,
			OverrideMonth: item.OverrideMonth,
		})
	}
	req.CountMode = countMode
	req.RoleOverrides = overrides
	return req, nil
}

func validateInterfaceQuotaLadder(scope string, hour, day, week, month int64) error {
	if hour > 0 && day > 0 && hour > day {
		return apperrors.New(consts.StatusBadRequest, scope+"每小时配额不能大于每日配额")
	}
	if day > 0 && week > 0 && day > week {
		return apperrors.New(consts.StatusBadRequest, scope+"每日配额不能大于每周配额")
	}
	if week > 0 && month > 0 && week > month {
		return apperrors.New(consts.StatusBadRequest, scope+"每周配额不能大于每月配额")
	}
	return nil
}
