package interfaceapi

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strings"

	apperrors "bugpk-api/internal/pkg/errors"

	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

const (
	degradeWindowDay   = "day"
	degradeWindowWeek  = "week"
	degradeWindowMonth = "month"
	degradeWindowTotal = "total"

	maxInterfaceDegradeThreshold int64 = 1_000_000_000_000
	maxInterfaceDegradeRoles           = 500
)

var interfaceDegradeWindows = map[string]bool{
	degradeWindowDay:   true,
	degradeWindowWeek:  true,
	degradeWindowMonth: true,
	degradeWindowTotal: true,
}

// InterfaceDegrade 读取接口的按用户组次数降级配置。
func (s *Service) InterfaceDegrade(ctx context.Context, principal Principal, id int64) (InterfaceDegradePolicy, error) {
	if err := s.require(ctx, principal, "interface:list"); err != nil {
		return InterfaceDegradePolicy{}, err
	}
	if _, err := s.repo.ByID(ctx, id); err != nil {
		return InterfaceDegradePolicy{}, mapNotFound(err, "接口不存在")
	}
	return s.repo.DegradePolicy(ctx, id)
}

// SaveInterfaceDegrade 保存降级配置。配置实质变化时递增 policy_version，
// 网关使用新版本的计数键，从而让用户在配置调整后重新开始计数。
func (s *Service) SaveInterfaceDegrade(ctx context.Context, principal Principal, id int64, req InterfaceDegradePolicy) error {
	if err := s.require(ctx, principal, "interface:update"); err != nil {
		return err
	}
	current, err := s.repo.ByID(ctx, id)
	if err != nil {
		return mapNotFound(err, "接口不存在")
	}
	policy, err := normalizeInterfaceDegrade(req)
	if err != nil {
		return err
	}
	if err := s.validateDegradeTargets(ctx, id, policy); err != nil {
		return err
	}
	existing, err := s.repo.DegradePolicy(ctx, id)
	if err != nil {
		return err
	}
	policy.PolicyVersion = existing.PolicyVersion
	if policy.PolicyVersion < 1 {
		policy.PolicyVersion = 1
	}
	if !sameInterfaceDegradePolicy(existing, policy) {
		policy.PolicyVersion++
	}
	if err := s.repo.SaveDegradePolicy(ctx, id, policy); err != nil {
		if errors.Is(err, ErrInterfaceDegradeRoleInvalid) {
			return apperrors.New(consts.StatusBadRequest, "降级策略引用了不存在的用户组")
		}
		if errors.Is(err, ErrInterfaceDegradeTargetInvalid) {
			return apperrors.New(consts.StatusBadRequest, "降级目标接口不存在或已禁用")
		}
		return err
	}
	s.notifyInterfaceChanged(current.APICode, current.APICode)
	return nil
}

func (s *Service) validateDegradeTargets(ctx context.Context, sourceID int64, policy InterfaceDegradePolicy) error {
	targets := make(map[int64]struct{}, len(policy.RoleOverrides)+1)
	if policy.DefaultTargetInterfaceID > 0 {
		targets[policy.DefaultTargetInterfaceID] = struct{}{}
	}
	for _, item := range policy.RoleOverrides {
		if item.TargetInterfaceID > 0 {
			targets[item.TargetInterfaceID] = struct{}{}
		}
	}
	for targetID := range targets {
		if targetID == sourceID {
			return apperrors.New(consts.StatusBadRequest, "降级目标接口不能是当前接口")
		}
		target, err := s.repo.ByID(ctx, targetID)
		if errors.Is(err, sql.ErrNoRows) {
			return apperrors.New(consts.StatusBadRequest, "降级目标接口不存在")
		}
		if err != nil {
			return err
		}
		if target.Status != 1 {
			return apperrors.New(consts.StatusBadRequest, "降级目标接口已禁用")
		}
	}
	return nil
}

func normalizeInterfaceDegrade(req InterfaceDegradePolicy) (InterfaceDegradePolicy, error) {
	if req.Enabled != 0 && req.Enabled != 1 {
		return InterfaceDegradePolicy{}, apperrors.New(consts.StatusBadRequest, "降级开关不合法")
	}
	if req.DefaultTargetInterfaceID < 0 {
		return InterfaceDegradePolicy{}, apperrors.New(consts.StatusBadRequest, "默认目标接口不合法")
	}
	if len(req.RoleOverrides) > maxInterfaceDegradeRoles {
		return InterfaceDegradePolicy{}, apperrors.New(consts.StatusBadRequest, "降级用户组配置不能超过 500 条")
	}
	defaultURL := strings.TrimSpace(req.DefaultTargetURL)
	if req.DefaultTargetInterfaceID > 0 && defaultURL != "" {
		return InterfaceDegradePolicy{}, apperrors.New(consts.StatusBadRequest, "默认降级目标只能选择接口或自定义地址之一")
	}
	if defaultURL != "" {
		if err := validateHTTPURL(defaultURL, 1024, "默认降级目标地址"); err != nil {
			return InterfaceDegradePolicy{}, err
		}
	}
	normalized := InterfaceDegradePolicy{
		Enabled:                  req.Enabled,
		DefaultTargetInterfaceID: req.DefaultTargetInterfaceID,
		DefaultTargetURL:         defaultURL,
		RoleOverrides:            make([]InterfaceRoleDegrade, 0, len(req.RoleOverrides)),
	}
	seen := make(map[int64]struct{}, len(req.RoleOverrides))
	for _, item := range req.RoleOverrides {
		if item.RoleID <= 0 {
			return InterfaceDegradePolicy{}, apperrors.New(consts.StatusBadRequest, "降级用户组的角色 ID 不合法")
		}
		if _, ok := seen[item.RoleID]; ok {
			return InterfaceDegradePolicy{}, apperrors.New(consts.StatusBadRequest, "同一用户组不能重复配置降级")
		}
		seen[item.RoleID] = struct{}{}
		// 阈值 0 等价于该用户组不降级，直接忽略，避免存无效行。
		if item.Threshold <= 0 {
			continue
		}
		if item.Threshold > maxInterfaceDegradeThreshold {
			return InterfaceDegradePolicy{}, apperrors.New(consts.StatusBadRequest, "降级阈值超出允许范围")
		}
		window := strings.ToLower(strings.TrimSpace(item.Window))
		if window == "" {
			window = degradeWindowDay
		}
		if !interfaceDegradeWindows[window] {
			return InterfaceDegradePolicy{}, apperrors.New(consts.StatusBadRequest, "降级计数窗口只能是 day/week/month/total")
		}
		targetID := item.TargetInterfaceID
		if targetID < 0 {
			return InterfaceDegradePolicy{}, apperrors.New(consts.StatusBadRequest, "用户组降级目标接口不合法")
		}
		targetURL := strings.TrimSpace(item.TargetURL)
		if targetID > 0 && targetURL != "" {
			return InterfaceDegradePolicy{}, apperrors.New(consts.StatusBadRequest, "用户组降级目标只能选择接口或自定义地址之一")
		}
		if targetURL != "" {
			if err := validateHTTPURL(targetURL, 1024, "用户组降级目标地址"); err != nil {
				return InterfaceDegradePolicy{}, err
			}
			if targetURL == normalized.DefaultTargetURL {
				targetURL = ""
			}
		}
		if targetID == normalized.DefaultTargetInterfaceID {
			targetID = 0
		}
		normalized.RoleOverrides = append(normalized.RoleOverrides, InterfaceRoleDegrade{
			RoleID:            item.RoleID,
			Window:            window,
			Threshold:         item.Threshold,
			TargetInterfaceID: targetID,
			TargetURL:         targetURL,
		})
	}
	sort.Slice(normalized.RoleOverrides, func(i, j int) bool {
		return normalized.RoleOverrides[i].RoleID < normalized.RoleOverrides[j].RoleID
	})
	if normalized.Enabled == 1 {
		if normalized.DefaultTargetInterfaceID <= 0 && normalized.DefaultTargetURL == "" {
			return InterfaceDegradePolicy{}, apperrors.New(consts.StatusBadRequest, "启用降级时必须选择默认目标接口或填写自定义地址")
		}
		if len(normalized.RoleOverrides) == 0 {
			return InterfaceDegradePolicy{}, apperrors.New(consts.StatusBadRequest, "启用降级时至少配置一个用户组阈值")
		}
	}
	return normalized, nil
}

// sameInterfaceDegradePolicy 只比较会影响运行时行为的字段；目标接口的名称、
// 角色名等展示字段变化不重置计数。
func sameInterfaceDegradePolicy(left, right InterfaceDegradePolicy) bool {
	if left.Enabled != right.Enabled ||
		left.DefaultTargetInterfaceID != right.DefaultTargetInterfaceID ||
		left.DefaultTargetURL != right.DefaultTargetURL ||
		len(left.RoleOverrides) != len(right.RoleOverrides) {
		return false
	}
	leftRows := append([]InterfaceRoleDegrade(nil), left.RoleOverrides...)
	rightRows := append([]InterfaceRoleDegrade(nil), right.RoleOverrides...)
	sort.Slice(leftRows, func(i, j int) bool { return leftRows[i].RoleID < leftRows[j].RoleID })
	sort.Slice(rightRows, func(i, j int) bool { return rightRows[i].RoleID < rightRows[j].RoleID })
	for index := range leftRows {
		leftRow, rightRow := leftRows[index], rightRows[index]
		if leftRow.RoleID != rightRow.RoleID ||
			leftRow.Window != rightRow.Window ||
			leftRow.Threshold != rightRow.Threshold ||
			leftRow.TargetInterfaceID != rightRow.TargetInterfaceID ||
			leftRow.TargetURL != rightRow.TargetURL {
			return false
		}
	}
	return true
}
