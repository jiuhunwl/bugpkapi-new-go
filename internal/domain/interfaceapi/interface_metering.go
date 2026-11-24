package interfaceapi

import (
	"context"
	"errors"
	"strings"

	apperrors "bugpk-api/internal/pkg/errors"

	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

// 计量策略取值。与 bp_interface_metering_policy 的 CHECK 约束一一对应。
const (
	meteringInherit      = "inherit"
	meteringAssetFree    = "free"
	meteringAssetCharge  = "charge"
	meteringConsume      = "consume"
	meteringSkip         = "skip"
	meteringGroupCount   = "count"
	maxMeteringRoleOverr = 500
)

var (
	assetMeteringModes   = map[string]bool{"": true, meteringInherit: true, meteringAssetFree: true, meteringAssetCharge: true}
	packageMeteringModes = map[string]bool{"": true, meteringInherit: true, meteringConsume: true, meteringSkip: true}
	groupMeteringModes   = map[string]bool{"": true, meteringInherit: true, meteringGroupCount: true, meteringSkip: true}
)

func (s *Service) InterfaceMetering(ctx context.Context, principal Principal, id int64) (InterfaceMeteringPolicy, error) {
	if err := s.require(ctx, principal, "interface:list"); err != nil {
		return InterfaceMeteringPolicy{}, err
	}
	if _, err := s.repo.ByID(ctx, id); err != nil {
		return InterfaceMeteringPolicy{}, mapNotFound(err, "接口不存在")
	}
	policy, err := s.repo.Metering(ctx, id)
	if err != nil {
		return InterfaceMeteringPolicy{}, err
	}
	quota, err := s.repo.InterfaceQuota(ctx, id)
	if err != nil {
		return InterfaceMeteringPolicy{}, err
	}
	// 兜底闸门状态随策略一起返回：三维全免时它是唯一的限流来源，
	// 前端据此在保存前就提示去配额页设置，而不是等保存报 400。
	policy.GuardEnabled = quota.Enabled
	policy.GuardAllUsersSet = quota.AllUsersHour > 0 || quota.AllUsersDay > 0 || quota.AllUsersWeek > 0 || quota.AllUsersMonth > 0
	return policy, nil
}

func (s *Service) SaveInterfaceMetering(ctx context.Context, principal Principal, id int64, req InterfaceMeteringPolicy) error {
	if err := s.require(ctx, principal, "interface:update"); err != nil {
		return err
	}
	current, err := s.repo.ByID(ctx, id)
	if err != nil {
		return mapNotFound(err, "接口不存在")
	}
	policy, err := normalizeInterfaceMetering(req)
	if err != nil {
		return err
	}
	quota, err := s.repo.InterfaceQuota(ctx, id)
	if err != nil {
		return err
	}
	if err := requireUnmeteredGuard(policy, quota); err != nil {
		return err
	}
	if err := s.repo.SaveMetering(ctx, id, policy); err != nil {
		if errors.Is(err, ErrInterfaceMeteringRoleInvalid) {
			return apperrors.New(consts.StatusBadRequest, "计量策略引用了不存在的用户组")
		}
		return err
	}
	s.notifyInterfaceChanged(current.APICode, current.APICode)
	return nil
}

func normalizeInterfaceMetering(req InterfaceMeteringPolicy) (InterfaceMeteringPolicy, error) {
	normalized := InterfaceMeteringPolicy{
		AssetMode:   normalizeMeteringMode(req.AssetMode),
		PackageMode: normalizeMeteringMode(req.PackageMode),
		GroupMode:   normalizeMeteringMode(req.GroupMode),
	}
	if !assetMeteringModes[normalized.AssetMode] {
		return InterfaceMeteringPolicy{}, apperrors.New(consts.StatusBadRequest, "资产扣减策略只能是 inherit/free/charge")
	}
	if !packageMeteringModes[normalized.PackageMode] {
		return InterfaceMeteringPolicy{}, apperrors.New(consts.StatusBadRequest, "套餐次数策略只能是 inherit/consume/skip")
	}
	if !groupMeteringModes[normalized.GroupMode] {
		return InterfaceMeteringPolicy{}, apperrors.New(consts.StatusBadRequest, "用户组免费额度策略只能是 inherit/count/skip")
	}
	if len(req.RoleOverrides) > maxMeteringRoleOverr {
		return InterfaceMeteringPolicy{}, apperrors.New(consts.StatusBadRequest, "用户组计量策略覆盖不能超过 500 条")
	}
	seen := make(map[int64]struct{}, len(req.RoleOverrides))
	for _, item := range req.RoleOverrides {
		asset := normalizeMeteringMode(item.AssetMode)
		pkg := normalizeMeteringMode(item.PackageMode)
		group := normalizeMeteringMode(item.GroupMode)
		if asset == meteringInherit && pkg == meteringInherit && group == meteringInherit {
			continue
		}
		if !assetMeteringModes[asset] || !packageMeteringModes[pkg] || !groupMeteringModes[group] {
			return InterfaceMeteringPolicy{}, apperrors.New(consts.StatusBadRequest, "用户组计量策略取值不合法")
		}
		if item.RoleID <= 0 {
			return InterfaceMeteringPolicy{}, apperrors.New(consts.StatusBadRequest, "用户组计量策略的角色 ID 不合法")
		}
		if _, ok := seen[item.RoleID]; ok {
			return InterfaceMeteringPolicy{}, apperrors.New(consts.StatusBadRequest, "同一用户组不能重复配置")
		}
		seen[item.RoleID] = struct{}{}
		normalized.RoleOverrides = append(normalized.RoleOverrides, InterfaceRoleMetering{
			RoleID:      item.RoleID,
			AssetMode:   asset,
			PackageMode: pkg,
			GroupMode:   group,
		})
	}
	return normalized, nil
}

func normalizeMeteringMode(mode string) string {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		return meteringInherit
	}
	return mode
}

// requireUnmeteredGuard 拦住「完全没有任何计量闸门」的配置。
//
// 三维全免后，网关对该请求路径既不看价格也不看套餐、不计免费额度窗口，只剩下
// 写死不可豁免的两层：注册后 24h 首日额度（只卡新号）和接口全平台总量。
// 老账号调老接口就变成完全无限，正是 1.2.86 之前「用户 101 调用 3521 次而
// day_used=0」的形态。因此要求该接口已启用接口配额并设置至少一项全平台总量。
//
// 用户组覆盖也按解析后的结果一并检查：只要存在任意一条完全免计量的路径，就需要兜底。
func requireUnmeteredGuard(policy InterfaceMeteringPolicy, quota InterfaceQuotaPolicy) error {
	effective := []InterfaceMeteringPolicy{{
		AssetMode:   policy.AssetMode,
		PackageMode: policy.PackageMode,
		GroupMode:   policy.GroupMode,
	}}
	for _, override := range policy.RoleOverrides {
		effective = append(effective, InterfaceMeteringPolicy{
			AssetMode:   firstNonInherit(normalizeMeteringMode(override.AssetMode), policy.AssetMode),
			PackageMode: firstNonInherit(normalizeMeteringMode(override.PackageMode), policy.PackageMode),
			GroupMode:   firstNonInherit(normalizeMeteringMode(override.GroupMode), policy.GroupMode),
		})
	}
	for _, item := range effective {
		if item.AssetMode != meteringAssetFree || item.PackageMode != meteringSkip || item.GroupMode != meteringSkip {
			continue
		}
		hasAllUsersCap := quota.Enabled == 1 &&
			(quota.AllUsersHour > 0 || quota.AllUsersDay > 0 || quota.AllUsersWeek > 0 || quota.AllUsersMonth > 0)
		if !hasAllUsersCap {
			return apperrors.New(consts.StatusBadRequest,
				"该配置会让接口完全不计量，必须先在接口配额启用总开关并设置至少一项全平台总量，否则没有任何限流兜底")
		}
	}
	return nil
}

func firstNonInherit(override, base string) string {
	if override != meteringInherit {
		return override
	}
	return base
}
